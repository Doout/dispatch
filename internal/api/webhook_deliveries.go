package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/oklog/ulid/v2"
)

const webhookRetryWindow = 7 * 24 * time.Hour
const webhookAttemptTimeout = 4 * time.Minute

var webhookDeliveryPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)

type webhookEnvelope struct {
	Action       string `json:"action"`
	Installation struct {
		ID    int64 `json:"id"`
		AppID int64 `json:"app_id"`
	} `json:"installation"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (a *API) webhookVault() *secretcrypto.Vault {
	if a.eventConfig.Vault != nil {
		return a.eventConfig.Vault
	}
	if a.eventConfig.GitHubApps != nil {
		return a.eventConfig.GitHubApps.Vault
	}
	return nil
}

// Only local validation and durable storage belong in the HTTP receiver. No
// repository access, build, cleanup, comment or external API call precedes ACK.
func (a *API) acceptGitHubDelivery(w http.ResponseWriter, r *http.Request, connectionID string, installationID int64, body []byte) {
	kind := r.Header.Get("X-GitHub-Event")
	if !slices.Contains([]string{"push", "issue_comment", "pull_request", "repository", "installation", "installation_repositories"}, kind) {
		w.WriteHeader(204)
		return
	}
	if kind == "push" && connectionID == "" {
		w.WriteHeader(204)
		return
	}
	id := strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	if !webhookDeliveryPattern.MatchString(id) {
		problem(w, 400, "Invalid delivery ID", "Send the GitHub delivery identifier.")
		return
	}
	var env webhookEnvelope
	if json.Unmarshal(body, &env) != nil {
		problem(w, 400, "Invalid webhook event", "Send a valid JSON event.")
		return
	}
	if connectionID != "" && env.Installation.ID < 1 {
		problem(w, 403, "Missing GitHub App installation", "The event must identify its installation.")
		return
	}
	if connectionID == "" && installationID > 0 && env.Installation.ID != installationID {
		problem(w, 403, "Unexpected installation", "The event does not belong to this connection.")
		return
	}
	now := time.Now().UTC()
	switch kind {
	case "push":
		if _, err := workflow.ParsePush(body); err != nil {
			problem(w, 400, "Invalid push event", err.Error())
			return
		}
	case "issue_comment", "pull_request":
		event, err := events.ParseGitHubEvent(kind, id, body, now)
		if errors.Is(err, events.ErrEventUnsupported) {
			w.WriteHeader(204)
			return
		}
		if err != nil {
			problem(w, 400, "Invalid webhook event", err.Error())
			return
		}
		if env.Action == "" || !validWebhookRepository(event.Repository) {
			problem(w, 400, "Invalid webhook event", "The event must identify its repository and action.")
			return
		}
		if kind == "issue_comment" {
			if commentID, err := strconv.ParseUint(event.SourceCommentID, 10, 64); err != nil || commentID == 0 {
				problem(w, 400, "Invalid comment event", "The event must identify its source comment.")
				return
			}
		}
	case "repository":
		if connectionID == "" {
			w.WriteHeader(204)
			return
		}
		if env.Action == "" || env.Repository.ID < 1 || !validWebhookRepository(env.Repository.FullName) {
			problem(w, 400, "Invalid repository event", "The event must identify its repository and action.")
			return
		}
	case "installation", "installation_repositories":
		if connectionID == "" {
			w.WriteHeader(204)
			return
		}
		if env.Action == "" {
			problem(w, 400, "Invalid installation event", "The event must identify its action.")
			return
		}
	}
	if a.webhookVault() == nil {
		problem(w, 503, "Encrypted webhook storage unavailable", "Configure the controller master key before receiving webhooks.")
		return
	}
	digest := sha256.Sum256(body)
	receipt := core.WebhookDelivery{ID: ulid.Make().String(), ConnectionID: connectionID, DeliveryID: id, BodyDigest: hex.EncodeToString(digest[:]), Event: kind, Repository: events.NormalizeRepository(env.Repository.FullName), InstallationID: env.Installation.ID, ReceivedAt: now, ExpiresAt: now.Add(webhookRetryWindow)}
	var err error
	receipt.Ciphertext, err = a.webhookVault().Encrypt("webhook-delivery:"+receipt.ID, body)
	if err != nil {
		a.internal(w, err)
		return
	}
	receipt.Activities, err = a.webhookReceiptActivities(r.Context(), receipt)
	if err != nil {
		a.internal(w, err)
		return
	}
	saved, created, err := a.store.AcceptWebhookDelivery(r.Context(), receipt)
	if errors.Is(err, store.ErrWebhookDeliveryConflict) {
		problem(w, 409, "Delivery ID conflict", "This identifier was already accepted with a different event or payload.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	writeJSON(w, status, struct {
		Receipt   core.WebhookDelivery `json:"receipt"`
		Duplicate bool                 `json:"duplicate"`
	}{saved, !created})
}

func validWebhookRepository(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && len(value) <= 250 && !strings.ContainsAny(value, " \t\r\n")
}

func (a *API) webhookReceiptActivities(ctx context.Context, r core.WebhookDelivery) ([]core.EventActivity, error) {
	rules, err := a.configuredEventRules(ctx)
	if err != nil {
		return nil, err
	}
	items := []core.EventActivity{}
	for _, rule := range rules {
		if !rule.Enabled || rule.ConnectionID != r.ConnectionID {
			continue
		}
		matched := false
		for _, repo := range rule.Repositories {
			if events.NormalizeRepository(repo) == r.Repository {
				matched = true
			}
		}
		if !matched {
			continue
		}
		projects := rule.ProjectIDs
		if len(rule.RepositoryProjects[r.Repository]) > 0 {
			projects = rule.RepositoryProjects[r.Repository]
		}
		for _, projectID := range projects {
			items = append(items, core.EventActivity{ID: ulid.Make().String(), ProjectID: projectID, RuleID: rule.ID, Name: rule.Name, Transport: "webhook", Kind: "webhook_delivery", Repository: r.Repository, CreatedAt: r.ReceivedAt})
		}
	}
	return items, nil
}

func (a *API) listWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	if len(before) > 64 {
		problem(w, 400, "Invalid cursor", "Use a receipt ID from the previous page.")
		return
	}
	items, err := a.store.ListWebhookDeliveries(r.Context(), before, 50)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, items)
}

func (a *API) RunWebhookProcessor(ctx context.Context) {
	// Resume legacy events accepted by earlier controllers as well as receipts
	// accepted before this process started. Lease expiry handles interrupted work.
	if a.workflows != nil {
		recovery, cancel := context.WithTimeout(ctx, webhookAttemptTimeout)
		if err := a.workflows.RecoverPushEvents(recovery); err != nil && a.logger != nil {
			a.logger.Warn("webhook push recovery pending")
		}
		cancel()
	}
	retain := func() {
		if err := a.store.RetainWebhookDeliveries(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil && a.logger != nil {
			a.logger.Warn("webhook retention pending")
		}
	}
	retain()
	maintenance := time.NewTicker(15 * time.Minute)
	defer maintenance.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := a.ProcessWebhooksOnce(ctx); err != nil && ctx.Err() == nil && a.logger != nil {
			a.logger.Warn("webhook queue processing pending")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-maintenance.C:
			retain()
		}
	}
}

func (a *API) ProcessWebhooksOnce(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		r, err := a.store.ClaimWebhookDelivery(ctx, time.Now().UTC(), webhookAttemptTimeout+time.Minute)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, webhookAttemptTimeout)
		result, err := a.executeWebhookDelivery(attempt, r)
		cancel()
		now := time.Now().UTC()
		r.Result = result
		r.State = "processed"
		r.Error = ""
		if err != nil {
			r.State = "retry"
			r.Error = err.Error()
			delay := time.Duration(1<<min(r.Attempts, 11)) * 5 * time.Second
			if delay > time.Hour {
				delay = time.Hour
			}
			r.NextAttemptAt = now.Add(delay)
			if errors.Is(err, errWebhookRejected) {
				r.State = "rejected"
			}
			if !r.NextAttemptAt.Before(r.ExpiresAt) {
				r.State = "expired"
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		} // leave receipt leased for restart recovery
		if err := a.store.FinishWebhookDelivery(ctx, r, now); err != nil {
			return err
		}
	}
	return nil
}

var errWebhookRejected = errors.New("Webhook rejected")

func (a *API) executeWebhookDelivery(ctx context.Context, r core.WebhookDelivery) (string, error) {
	vault := a.webhookVault()
	if vault == nil {
		return "", errors.New("Encrypted webhook storage unavailable; restore the controller master key before retrying.")
	}
	body, err := vault.Decrypt("webhook-delivery:"+r.ID, r.Ciphertext)
	if err != nil {
		return "", errors.New("Cannot decrypt delivery; restore the controller master key before retrying.")
	}
	var env webhookEnvelope
	if json.Unmarshal(body, &env) != nil {
		return "", fmt.Errorf("%w: invalid saved payload", errWebhookRejected)
	}
	if r.Event == "repository" || r.Event == "installation" || r.Event == "installation_repositories" {
		if a.eventConfig.GitHubApps == nil {
			return "", errors.New("GitHub App service is unavailable; retry scheduled.")
		}
		connection, err := a.store.GetGitHubApp(ctx, r.ConnectionID)
		if err != nil {
			return "", fmt.Errorf("%w: connection no longer exists", errWebhookRejected)
		}
		if env.Installation.AppID != 0 && env.Installation.AppID != connection.AppID {
			return "", fmt.Errorf("%w: installation belongs to another App", errWebhookRejected)
		}
		a.eventConfig.GitHubApps.Invalidate(r.ConnectionID)
		if r.Event == "repository" && env.Action == "deleted" {
			// Signed deletion is checked against the previously pinned repository ID.
			// A live installation lookup would be incorrect after deletion (404).
			sources, err := a.store.ListConfigSources(ctx)
			if err != nil {
				return "", errors.New("Repository identity lookup failed; retry scheduled.")
			}
			bound := false
			for _, source := range sources {
				if source.GitHubAppID == r.ConnectionID && source.RepositoryID > 0 && source.RepositoryID == env.Repository.ID {
					bound = true
					break
				}
			}
			if !bound {
				return "", fmt.Errorf("%w: deleted repository is not bound to this App", errWebhookRejected)
			}
			if err := a.store.RecordDeletedRepository(ctx, r.ConnectionID, env.Repository.ID, env.Repository.FullName, r.DeliveryID, r.ReceivedAt); err != nil {
				return "", errors.New("Repository deletion evidence could not be recorded; retry scheduled.")
			}
			return "Repository deletion recorded; installation cache invalidated.", nil
		}
		return "Installation cache invalidated.", nil
	}
	if r.ConnectionID != "" {
		if a.eventConfig.GitHubApps == nil {
			return "", errors.New("GitHub App service is unavailable; retry scheduled.")
		}
		installation, err := a.eventConfig.GitHubApps.RepositoryInstallation(ctx, r.ConnectionID, r.Repository)
		if err != nil {
			return "", errors.New("Repository installation lookup failed; retry scheduled.")
		}
		if installation != r.InstallationID {
			return "", fmt.Errorf("%w: repository installation does not match this App", errWebhookRejected)
		}
	}
	if r.Event == "push" {
		ids, err := a.workflows.ProcessPushDelivery(ctx, r.ConnectionID, r.ID, body)
		if err != nil {
			return "", errors.New("Push workflow processing failed; inspect source and workflow activity. Retry scheduled.")
		}
		result, _ := json.Marshal(struct {
			RevisionIDs []string `json:"revisionIds"`
		}{ids})
		return string(result), nil
	}
	event, err := events.ParseGitHubEvent(r.Event, r.DeliveryID, body, r.ReceivedAt)
	if err != nil {
		return "", fmt.Errorf("%w: invalid event", errWebhookRejected)
	}
	event.ProviderConnectionID = r.ConnectionID
	event.DeliveryID = events.CommentDeliveryID(event)
	groupService, eventService := a.groups, a.events
	if r.ConnectionID != "" {
		services, err := a.githubAppServices(ctx, r.ConnectionID)
		if err != nil {
			return "", errors.New("Preview services unavailable; retry scheduled.")
		}
		groupService, eventService = services.groups, services.events
	}
	target := &previewPollTarget{connectionID: r.ConnectionID, repository: r.Repository, transport: "webhook"}
	if err := a.previewDeliveryActivity(ctx, target, event, "running", nil); err != nil {
		return "", errors.New("Cannot record preview activity; retry scheduled.")
	}
	result, err := a.consumeGitHubPreviewEvent(ctx, event, groupService, eventService)
	state := "processed"
	if err != nil {
		state = "failed"
	}
	activityErr := a.previewDeliveryActivity(ctx, target, event, state, err)
	if err != nil || activityErr != nil {
		return "", errors.New("Preview processing failed; inspect the preview activity and cleanup status. Retry scheduled.")
	}
	// Persist only action identities; never the incoming comment arguments,
	// notifier text, rendered inputs or provider response bodies.
	actions := struct {
		Duplicate   bool     `json:"duplicate"`
		Ignored     bool     `json:"ignored"`
		PreviewIDs  []string `json:"previewIds,omitempty"`
		GroupRunIDs []string `json:"groupRunIds,omitempty"`
	}{Duplicate: result.Duplicate, Ignored: result.Ignored}
	for _, preview := range result.Previews {
		actions.PreviewIDs = append(actions.PreviewIDs, preview.ID)
	}
	for _, run := range result.PreviewGroupRuns {
		actions.GroupRunIDs = append(actions.GroupRunIDs, run.ID)
	}
	encoded, _ := json.Marshal(actions)
	return string(encoded), nil
}
