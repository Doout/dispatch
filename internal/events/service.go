package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

var (
	ErrSignatureMissing = errors.New("webhook signature missing")
	ErrSignatureInvalid = errors.New("webhook signature invalid")
	ErrEventUnsupported = errors.New("webhook event unsupported")
)

type Store interface {
	IncomingEventExists(context.Context, core.EventProvider, string) (bool, error)
	HasEventTrigger(context.Context, core.EventProvider, string, string, string) (bool, error)
	ProcessIncomingEvent(context.Context, core.IncomingEvent) (core.EventResult, error)
	GetPreviewEnvironment(context.Context, string) (core.PreviewEnvironment, error)
	TransitionPreviewEnvironment(context.Context, core.PreviewEnvironment, ...core.PreviewState) (bool, error)
}

type SourceRevision struct {
	HeadRef string
	HeadSHA string
	BaseRef string
	Open    bool
}

type PullRequestResolver interface {
	ResolvePullRequest(context.Context, string, int) (SourceRevision, error)
}

type StartResult struct {
	DeploymentID string
	AppID        string
	URL          string
	Message      string
	Completion   <-chan Completion
}

type Completion struct {
	State   core.PreviewState
	Message string
	URL     string
}

// Lifecycle is implemented by a deployment backend. A webhook records durable
// intent before either callback runs, so implementations can safely enqueue work.
type Lifecycle interface {
	StartPreview(context.Context, core.PreviewEnvironment) (StartResult, error)
	ResumePreview(core.PreviewEnvironment) <-chan Completion
	CleanupPreview(context.Context, core.PreviewEnvironment) error
}

type Notification struct {
	Preview core.PreviewEnvironment
	State   core.PreviewState
	Message string
	URL     string
}

// Notifier lets a provider integration create or update the status comment.
// Returning a comment ID links later lifecycle updates to that same comment.
type Notifier interface {
	UpdatePreview(context.Context, Notification) (statusCommentID string, err error)
}

type Service struct {
	store     Store
	resolver  PullRequestResolver
	lifecycle Lifecycle
	notifier  Notifier
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
	watching  map[string]bool
}

func New(store Store, resolver PullRequestResolver, lifecycle Lifecycle, notifier Notifier) *Service {
	return &Service{store: store, resolver: resolver, lifecycle: lifecycle, notifier: notifier,
		locks: make(map[string]*sync.Mutex), watching: make(map[string]bool)}
}

func VerifySignature(secret string, body []byte, signature string) error {
	if signature == "" {
		return ErrSignatureMissing
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil || !strings.HasPrefix(signature, "sha256=") {
		return ErrSignatureInvalid
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrSignatureInvalid
	}
	return nil
}

func ParseGitHubEvent(eventName, deliveryID string, body []byte, now time.Time) (core.IncomingEvent, error) {
	var payload struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
		Issue struct {
			Number      int             `json:"number"`
			PullRequest json.RawMessage `json:"pull_request"`
		} `json:"issue"`
		Comment struct {
			ID                json.Number `json:"id"`
			Body              string      `json:"body"`
			AuthorAssociation string      `json:"author_association"`
			User              struct {
				Login string `json:"login"`
			} `json:"user"`
		} `json:"comment"`
		PullRequest struct {
			Number int `json:"number"`
			Head   struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return core.IncomingEvent{}, fmt.Errorf("decode webhook: %w", err)
	}
	event := core.IncomingEvent{
		ID: ulid.Make().String(), Provider: core.EventProviderGitHub, DeliveryID: strings.TrimSpace(deliveryID),
		Action: payload.Action, Repository: NormalizeRepository(payload.Repository.FullName), Actor: payload.Sender.Login,
		ReceivedAt: now.UTC(),
	}
	if event.DeliveryID == "" || event.Repository == "" {
		return core.IncomingEvent{}, errors.New("webhook delivery and repository are required")
	}
	switch eventName {
	case "issue_comment":
		if len(payload.Issue.PullRequest) == 0 || string(payload.Issue.PullRequest) == "null" {
			return core.IncomingEvent{}, ErrEventUnsupported
		}
		event.Kind = core.EventKindPullRequestComment
		event.PullRequestNumber = payload.Issue.Number
		event.Actor = payload.Comment.User.Login
		event.ActorAssociation = strings.ToUpper(strings.TrimSpace(payload.Comment.AuthorAssociation))
		event.TrustedActor = trustedAuthorAssociation(event.ActorAssociation)
		event.SourceCommentID = payload.Comment.ID.String()
		event.Command, event.Arguments = ParseCommand(payload.Comment.Body)
	case "pull_request":
		event.Kind = core.EventKindPullRequest
		event.PullRequestNumber = payload.PullRequest.Number
		event.HeadRef = payload.PullRequest.Head.Ref
		event.HeadSHA = payload.PullRequest.Head.SHA
		event.BaseRef = payload.PullRequest.Base.Ref
	default:
		return core.IncomingEvent{}, ErrEventUnsupported
	}
	if event.PullRequestNumber < 1 {
		return core.IncomingEvent{}, errors.New("pull request number is required")
	}
	return event, nil
}

func trustedAuthorAssociation(association string) bool {
	switch association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func ParseCommand(message string) (command, arguments string) {
	line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(message, "\r\n", "\n"), "\n", 2)[0])
	fields := strings.Fields(line)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return "", ""
	}
	command = fields[0]
	if len(fields) > 1 {
		arguments = strings.TrimSpace(strings.TrimPrefix(line, command))
	}
	return command, arguments
}

func NormalizeRepository(repository string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(repository), "/"))
}

func NormalizeCommand(command, fallback string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		command = fallback
	}
	if !strings.HasPrefix(command, "/") || strings.ContainsAny(command, " \t\r\n") || len(command) > 64 {
		return "", errors.New("command must start with / and contain no whitespace")
	}
	return command, nil
}

func (s *Service) Process(ctx context.Context, event core.IncomingEvent) (core.EventResult, error) {
	if event.Kind == core.EventKindPullRequestComment && event.Action == "created" && event.Command != "" && event.TrustedActor && event.HeadSHA == "" && s.resolver != nil {
		exists, err := s.store.IncomingEventExists(ctx, event.Provider, event.DeliveryID)
		if err != nil {
			return core.EventResult{}, err
		}
		matches, err := s.store.HasEventTrigger(ctx, event.Provider, event.Repository, event.Command, event.ProviderConnectionID)
		if err != nil {
			return core.EventResult{}, err
		}
		if !exists && matches {
			revision, err := s.resolver.ResolvePullRequest(ctx, event.Repository, event.PullRequestNumber)
			if err != nil {
				return core.EventResult{}, fmt.Errorf("resolve pull request: %w", err)
			}
			event.HeadRef, event.HeadSHA, event.BaseRef = revision.HeadRef, revision.HeadSHA, revision.BaseRef
		}
	}
	result, err := s.store.ProcessIncomingEvent(ctx, event)
	if err != nil || result.Ignored {
		return result, err
	}
	for index := range result.Previews {
		preview, reconcileErr := s.reconcile(ctx, result.Previews[index].ID)
		if reconcileErr != nil {
			result.Previews[index] = preview
			return result, reconcileErr
		}
		result.Previews[index] = preview
	}
	return result, nil
}

func (s *Service) reconcile(ctx context.Context, id string) (core.PreviewEnvironment, error) {
	lock := s.previewLock(id)
	lock.Lock()
	defer lock.Unlock()
	preview, err := s.store.GetPreviewEnvironment(ctx, id)
	if err != nil {
		return preview, err
	}
	switch preview.State {
	case core.PreviewRequested:
		return s.start(ctx, preview)
	case core.PreviewDeploying:
		return s.resume(ctx, preview)
	case core.PreviewCleanupRequested:
		return s.cleanup(ctx, preview)
	case core.PreviewStarting:
		if time.Since(preview.UpdatedAt) > 5*time.Minute {
			preview.State, preview.Message, preview.UpdatedAt = core.PreviewRequested, "Retrying interrupted preview startup", time.Now().UTC()
			if changed, err := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewStarting); err != nil || !changed {
				return preview, err
			}
			return s.start(ctx, preview)
		}
	case core.PreviewCleaning:
		if time.Since(preview.UpdatedAt) > 5*time.Minute {
			preview.State, preview.Message, preview.UpdatedAt = core.PreviewCleanupRequested, "Retrying interrupted preview cleanup", time.Now().UTC()
			if changed, err := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewCleaning); err != nil || !changed {
				return preview, err
			}
			return s.cleanup(ctx, preview)
		}
	}
	return preview, nil
}

func (s *Service) start(ctx context.Context, preview core.PreviewEnvironment) (core.PreviewEnvironment, error) {
	preview.State, preview.Message, preview.UpdatedAt = core.PreviewStarting, "Preparing preview deployment", time.Now().UTC()
	claimed, err := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewRequested)
	if err != nil || !claimed {
		return s.store.GetPreviewEnvironment(ctx, preview.ID)
	}
	if s.lifecycle == nil {
		return preview, nil
	}
	started, err := s.lifecycle.StartPreview(ctx, preview)
	if err != nil {
		preview.State, preview.Message, preview.UpdatedAt = core.PreviewRequested, err.Error(), time.Now().UTC()
		_, transitionErr := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewStarting)
		if transitionErr != nil {
			return preview, transitionErr
		}
		return preview, err
	}
	preview.AppID, preview.DeploymentID, preview.URL = started.AppID, started.DeploymentID, started.URL
	preview.State, preview.Message, preview.UpdatedAt = core.PreviewDeploying, started.Message, time.Now().UTC()
	deployed, err := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewStarting)
	if err != nil {
		_ = s.lifecycle.CleanupPreview(context.Background(), preview)
		return preview, err
	}
	if !deployed {
		current, loadErr := s.store.GetPreviewEnvironment(ctx, preview.ID)
		if loadErr != nil {
			return preview, loadErr
		}
		if current.State == core.PreviewCleanupRequested {
			current.AppID, current.DeploymentID, current.URL, current.UpdatedAt = preview.AppID, preview.DeploymentID, preview.URL, time.Now().UTC()
			if attached, attachErr := s.store.TransitionPreviewEnvironment(ctx, current, core.PreviewCleanupRequested); attachErr != nil {
				return current, attachErr
			} else if attached {
				return s.cleanup(ctx, current)
			}
		}
		// The start lost ownership. Compensate so an untracked instance cannot remain live.
		_ = s.lifecycle.CleanupPreview(context.Background(), preview)
		return current, nil
	}
	if err := s.notifyAndPersist(ctx, &preview, core.PreviewDeploying); err != nil {
		return preview, err
	}
	completion := started.Completion
	if completion == nil {
		completion = s.lifecycle.ResumePreview(preview)
	}
	s.watch(preview, completion)
	return preview, nil
}

func (s *Service) resume(ctx context.Context, preview core.PreviewEnvironment) (core.PreviewEnvironment, error) {
	if err := s.notifyAndPersist(ctx, &preview, core.PreviewDeploying); err != nil {
		return preview, err
	}
	if s.lifecycle != nil {
		s.watch(preview, s.lifecycle.ResumePreview(preview))
	}
	return preview, nil
}

func (s *Service) cleanup(ctx context.Context, preview core.PreviewEnvironment) (core.PreviewEnvironment, error) {
	preview.State, preview.Message, preview.UpdatedAt = core.PreviewCleaning, "Removing preview", time.Now().UTC()
	claimed, err := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewCleanupRequested)
	if err != nil || !claimed {
		return s.store.GetPreviewEnvironment(ctx, preview.ID)
	}
	if preview.AppID != "" && s.lifecycle != nil {
		if err := s.lifecycle.CleanupPreview(ctx, preview); err != nil {
			preview.State, preview.Message, preview.UpdatedAt = core.PreviewCleanupRequested, err.Error(), time.Now().UTC()
			_, transitionErr := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewCleaning)
			if transitionErr != nil {
				return preview, transitionErr
			}
			return preview, err
		}
		preview.AppID = ""
	}
	now := time.Now().UTC()
	preview.State, preview.Message, preview.ClosedAt, preview.UpdatedAt = core.PreviewClosed, "Preview removed", &now, now
	if s.notifier != nil {
		commentID, notifyErr := s.notifier.UpdatePreview(ctx, Notification{Preview: preview, State: preview.State, Message: preview.Message, URL: preview.URL})
		if notifyErr != nil {
			preview.State, preview.Message, preview.UpdatedAt = core.PreviewCleanupRequested, notifyErr.Error(), time.Now().UTC()
			_, transitionErr := s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewCleaning)
			if transitionErr != nil {
				return preview, transitionErr
			}
			return preview, fmt.Errorf("update preview status: %w", notifyErr)
		}
		if commentID != "" {
			preview.StatusCommentID = commentID
		}
	}
	_, err = s.store.TransitionPreviewEnvironment(ctx, preview, core.PreviewCleaning)
	return preview, err
}

func (s *Service) notifyAndPersist(ctx context.Context, preview *core.PreviewEnvironment, expected core.PreviewState) error {
	if s.notifier == nil || preview.StatusCommentID != "" {
		return nil
	}
	commentID, err := s.notifier.UpdatePreview(ctx, Notification{Preview: *preview, State: preview.State, Message: preview.Message, URL: preview.URL})
	if err != nil {
		return fmt.Errorf("update preview status: %w", err)
	}
	preview.StatusCommentID, preview.UpdatedAt = commentID, time.Now().UTC()
	changed, err := s.store.TransitionPreviewEnvironment(ctx, *preview, expected)
	if err != nil {
		return err
	}
	if !changed {
		return errors.New("preview state changed while saving its status comment")
	}
	return nil
}

func (s *Service) previewLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[id] == nil {
		s.locks[id] = &sync.Mutex{}
	}
	return s.locks[id]
}

func (s *Service) watch(preview core.PreviewEnvironment, completion <-chan Completion) {
	if completion == nil {
		return
	}
	s.mu.Lock()
	if s.watching[preview.ID] {
		s.mu.Unlock()
		return
	}
	s.watching[preview.ID] = true
	s.mu.Unlock()
	go s.awaitCompletion(preview.ID, completion)
}

func (s *Service) awaitCompletion(id string, completion <-chan Completion) {
	defer func() {
		s.mu.Lock()
		delete(s.watching, id)
		s.mu.Unlock()
	}()
	completed, ok := <-completion
	if !ok {
		return
	}
	lock := s.previewLock(id)
	lock.Lock()
	defer lock.Unlock()
	preview, err := s.store.GetPreviewEnvironment(context.Background(), id)
	if err != nil || preview.State != core.PreviewDeploying {
		return
	}
	preview.State, preview.Message = completed.State, completed.Message
	if completed.URL != "" {
		preview.URL = completed.URL
	}
	preview.UpdatedAt = time.Now().UTC()
	changed, err := s.store.TransitionPreviewEnvironment(context.Background(), preview, core.PreviewDeploying)
	if err != nil || !changed {
		return
	}
	if s.notifier != nil {
		for attempt := 0; attempt < 3; attempt++ {
			commentID, notifyErr := s.notifier.UpdatePreview(context.Background(), Notification{Preview: preview, State: preview.State, Message: preview.Message, URL: preview.URL})
			if notifyErr == nil {
				if commentID != "" && commentID != preview.StatusCommentID {
					preview.StatusCommentID, preview.UpdatedAt = commentID, time.Now().UTC()
					_, _ = s.store.TransitionPreviewEnvironment(context.Background(), preview, completed.State)
				}
				return
			}
			if attempt < 2 {
				time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
			}
		}
	}
}
