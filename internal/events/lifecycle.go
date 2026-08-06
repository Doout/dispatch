package events

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type DeploymentLifecycle struct {
	Store       store.Store
	Deployments *deploy.Service
	PollEvery   time.Duration
}

func (l DeploymentLifecycle) StartPreview(ctx context.Context, preview core.PreviewEnvironment) (StartResult, error) {
	template, err := l.Store.GetApp(ctx, preview.TemplateAppID)
	if err != nil {
		return StartResult{}, err
	}
	instance := previewApplication(template, preview)
	if err := l.Store.CreateApp(ctx, instance); err != nil {
		return StartResult{}, fmt.Errorf("create preview application: %w", err)
	}
	deployment, err := l.Deployments.Start(ctx, instance.ID, preview.HeadSHA)
	if err != nil {
		_ = l.Store.DeleteApp(context.Background(), instance.ID)
		return StartResult{}, err
	}
	return StartResult{AppID: instance.ID, DeploymentID: deployment.ID, URL: publicURL(instance.Domain), Message: "Preview deployment started"}, nil
}

func (l DeploymentLifecycle) ResumePreview(preview core.PreviewEnvironment) <-chan Completion {
	if preview.DeploymentID == "" {
		return nil
	}
	completion := make(chan Completion, 1)
	go l.watchDeployment(preview.DeploymentID, preview.URL, completion)
	return completion
}

func (l DeploymentLifecycle) CleanupPreview(ctx context.Context, preview core.PreviewEnvironment) error {
	active, err := l.Store.ActiveDeploymentForApp(ctx, preview.AppID)
	if err != nil {
		return err
	}
	if active != nil {
		if err := l.Deployments.Cancel(ctx, active.ID); err != nil {
			now := time.Now().UTC()
			active.State, active.Message, active.FinishedAt, active.LeaseUntil = core.DeploymentCancelled, "Deployment abandoned during preview cleanup", &now, nil
			if updateErr := l.Store.UpdateDeployment(ctx, *active); updateErr != nil {
				return updateErr
			}
		}
		if err := l.waitUntilInactive(ctx, preview.AppID); err != nil {
			return err
		}
	}
	if err := l.Deployments.Cleanup(ctx, preview.AppID, nil); err != nil && !errors.Is(err, deploy.ErrCleanupUnsupported) {
		return err
	}
	return l.Store.DeleteApp(ctx, preview.AppID)
}

func (l DeploymentLifecycle) waitUntilInactive(ctx context.Context, appID string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		active, err := l.Store.ActiveDeploymentForApp(ctx, appID)
		if err != nil {
			return err
		}
		if active == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l DeploymentLifecycle) watchDeployment(id, previewURL string, completion chan<- Completion) {
	defer close(completion)
	interval := l.PollEvery
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		deployment, err := l.Store.GetDeployment(context.Background(), id)
		if err != nil {
			completion <- Completion{State: core.PreviewFailed, Message: err.Error()}
			return
		}
		if deployment.State.Terminal() {
			state := core.PreviewFailed
			if deployment.State == core.DeploymentSucceeded {
				state = core.PreviewReady
			} else if deployment.State == core.DeploymentCancelled {
				state = core.PreviewClosed
			}
			completion <- Completion{State: state, Message: deployment.Message, URL: publicURL(previewURL)}
			return
		}
		<-ticker.C
	}
}

var previewNameCharacters = regexp.MustCompile(`[^a-z0-9-]+`)

func previewApplication(template core.App, preview core.PreviewEnvironment) core.App {
	instance := template
	instance.ID = ulid.Make().String()
	instance.Generated = true
	instance.Template = false
	suffix := "pr-" + strconv.Itoa(preview.PullRequestNumber)
	base := strings.Trim(previewNameCharacters.ReplaceAllString(strings.ToLower(template.Name), "-"), "-")
	instance.Name = trimDNSName(base+"-"+suffix, 63)
	instance.Branch = preview.HeadRef
	instance.HelmRelease = trimDNSName(strings.Trim(previewNameCharacters.ReplaceAllString(strings.ToLower(template.HelmRelease), "-"), "-")+"-"+suffix, 53)
	if strings.HasPrefix(instance.HelmRelease, "-") || instance.HelmRelease == suffix {
		instance.HelmRelease = trimDNSName(base+"-"+suffix, 53)
	}
	instance.Domain = renderPreviewValue(template.Domain, preview)
	instance.PreDeployHook = preview.PreDeployHook
	instance.PostDeployHook = preview.PostDeployHook
	instance.HookEnvironment = preview.HookEnvironment
	instance.State = "preview"
	instance.CreatedAt = time.Now().UTC()
	return instance
}

func renderPreviewValue(value string, preview core.PreviewEnvironment) string {
	replacer := strings.NewReplacer(
		"{pr}", strconv.Itoa(preview.PullRequestNumber),
		"{branch}", normalizeTemplateValue(preview.HeadRef),
		"{sha}", shortRevision(preview.HeadSHA),
	)
	return replacer.Replace(value)
}

func normalizeTemplateValue(value string) string {
	return strings.Trim(previewNameCharacters.ReplaceAllString(strings.ToLower(value), "-"), "-")
}

func shortRevision(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func trimDNSName(value string, limit int) string {
	value = strings.Trim(value, "-")
	if len(value) > limit {
		value = strings.Trim(value[:limit], "-")
	}
	if value == "" {
		return "preview"
	}
	return value
}

func publicURL(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" || strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
		return domain
	}
	return "https://" + domain
}
