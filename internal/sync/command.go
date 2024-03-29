package sync

import (
	"fmt"

	"os"

	"github.com/google/uuid"
	githubWrapper "github.com/kubefirst/git-helper/internal/github"
	gitlabWrapper "github.com/kubefirst/git-helper/internal/gitlab"
	"github.com/kubefirst/git-helper/internal/kubernetes"
	log "github.com/sirupsen/logrus"
	"github.com/xanzy/go-gitlab"
)

const (
	atlantisNamespace         = "atlantis"
	atlantisSecretName        = "atlantis-secrets"
	ngrokConfigMapName        = "ngrok"
	ngrokTriggerConfigMapName = "ngrok-trigger"
	ngrokExistingTunnelKey    = "active-ngrok-tunnel-url"
	ngrokExistingTriggerKey   = "trigger-ngrok-reload"
)

// Key to match for Atlantis Vault secret
// Webhook token key
var atlantisSecretTokenKey string

// DeleteWebhook
func DeleteWebhook(req WebhookOptions) error {
	switch req.Provider {
	case "github":
		gh := githubWrapper.NewGitHubClient(os.Getenv("GITHUB_TOKEN"))
		request := githubWrapper.RepositoryHookRequest{
			Org:        req.Owner,
			Repository: req.Repository,
			Url:        req.Url,
			Token:      req.Token,
		}
		err := gh.DeleteRepositoryWebhook(request)
		if err != nil {
			return err
		}
	case "gitlab":
		gitlabClient, err := gitlabWrapper.NewGitLabClient(os.Getenv("GIT_TOKEN"), req.Owner)
		if err != nil {
			return err
		}

		var enabled bool = true
		request := &gitlabWrapper.ProjectHookRequest{
			ProjectName: req.Repository,
			CreateOpts: &gitlab.AddProjectHookOptions{
				MergeRequestsEvents: &enabled,
				NoteEvents:          &enabled,
				PushEvents:          &enabled,
				URL:                 &req.Url,
			},
		}
		err = gitlabClient.DeleteProjectWebhook(request)
		if err != nil {
			return err
		}
	}

	return nil
}

// SynchronizeAtlantisWebhook
func SynchronizeAtlantisWebhook(req WebhookOptions) error {
	if req.Restart {
		log.Info("editing ngrok trigger ConfigMap to trigger restart")
		uuid := uuid.New()
		// Set the trigger configmap key value to a random uuid to trigger a reload
		err := kubernetes.UpdateConfigMapV2(req.KubeInClusterConfig, atlantisNamespace, ngrokTriggerConfigMapName, ngrokExistingTriggerKey, uuid.String())
		if err != nil {
			return err
		}
	}
	switch req.Provider {
	case "github":
		gh := githubWrapper.NewGitHubClient(os.Getenv("GIT_TOKEN"))
		atlantisSecretTokenKey = "ATLANTIS_GH_WEBHOOK_SECRET"

		// Use ConfigMap to get existing tunnel url if one exists
		configmap, err := kubernetes.ReadConfigMapV2(req.KubeInClusterConfig, atlantisNamespace, ngrokConfigMapName)
		if err != nil {
			return err
		}

		// Delete webhook if it exists
		var url string = fmt.Sprintf("%s/events", configmap[ngrokExistingTunnelKey])
		if configmap[ngrokExistingTunnelKey] != "placeholder" {
			request := githubWrapper.RepositoryHookRequest{
				Org:        req.Owner,
				Repository: req.Repository,
				Url:        url,
			}
			err = gh.DeleteRepositoryWebhook(request)
			if err != nil {
				log.Errorf("error deleting existing webhook: %s", err)
			}
		} else {
			log.Info("configmap entry is placeholder value, creating initial webhook token")
		}

		if !req.Cleanup {
			// Get new tunnel address
			newWebhookEndpoint, err := GetNgrokTunnelURL(ngrokAPIAddr)
			if err != nil {
				return err
			}

			// Get webhook token from Atlantis secret
			secret, err := kubernetes.ReadSecretV2(req.KubeInClusterConfig, atlantisNamespace, atlantisSecretName)
			if err != nil {
				return err
			}

			// Create new repository secret
			request := githubWrapper.RepositoryHookRequest{
				Org:        req.Owner,
				Repository: req.Repository,
				Url:        fmt.Sprintf("%s/events", newWebhookEndpoint),
				Token:      secret[atlantisSecretTokenKey],
			}
			err = gh.CreateRepositoryWebhook(request)
			if err != nil {
				return err
			}

			err = kubernetes.UpdateConfigMapV2(req.KubeInClusterConfig, atlantisNamespace, ngrokConfigMapName, ngrokExistingTunnelKey, newWebhookEndpoint)
			if err != nil {
				log.Error(err)
				return err
			}
		}
	case "gitlab":
		gitlabClient, err := gitlabWrapper.NewGitLabClient(os.Getenv("GIT_TOKEN"), req.Owner)
		if err != nil {
			return err
		}
		atlantisSecretTokenKey = "ATLANTIS_GITLAB_WEBHOOK_SECRET"

		// Use ConfigMap to get existing tunnel url if one exists
		configmap, err := kubernetes.ReadConfigMapV2(req.KubeInClusterConfig, atlantisNamespace, ngrokConfigMapName)
		if err != nil {
			return err
		}

		// Delete existing webhook if it exists
		var url string = fmt.Sprintf("%s/events", configmap[ngrokExistingTunnelKey])
		if configmap[ngrokExistingTunnelKey] != "placeholder" {
			request := &gitlabWrapper.ProjectHookRequest{
				ProjectName: req.Repository,
				CreateOpts: &gitlab.AddProjectHookOptions{
					URL: &url,
				},
			}
			err = gitlabClient.DeleteProjectWebhook(request)
			if err != nil {
				log.Errorf("error deleting existing webhook: %s", err)
			}
		} else {
			log.Info("configmap entry is placeholder value, creating initial webhook token")
		}

		if !req.Cleanup {
			// Get new tunnel address
			newWebhookEndpoint, err := GetNgrokTunnelURL(ngrokAPIAddr)
			if err != nil {
				return err
			}

			// Get webhook token from Atlantis secret
			secret, err := kubernetes.ReadSecretV2(req.KubeInClusterConfig, atlantisNamespace, atlantisSecretName)
			if err != nil {
				return err
			}

			// Create new repository secret
			var enabled bool = true
			var webhookURL string = fmt.Sprintf("%s/events", newWebhookEndpoint)
			var token string = secret[atlantisSecretTokenKey]

			request := &gitlabWrapper.ProjectHookRequest{
				ProjectName: req.Repository,
				CreateOpts: &gitlab.AddProjectHookOptions{
					MergeRequestsEvents: &enabled,
					NoteEvents:          &enabled,
					PushEvents:          &enabled,
					Token:               &token,
					URL:                 &webhookURL,
				},
			}
			err = gitlabClient.CreateProjectWebhook(request)
			if err != nil {
				return err
			}

			err = kubernetes.UpdateConfigMapV2(req.KubeInClusterConfig, atlantisNamespace, ngrokConfigMapName, ngrokExistingTunnelKey, newWebhookEndpoint)
			if err != nil {
				log.Error(err)
				return err
			}
		}

	}

	return nil
}
