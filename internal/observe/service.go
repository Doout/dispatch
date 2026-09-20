// Package observe coordinates observations. It never repairs or deploys resources.
package observe

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/drift"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type Repository interface {
	GetObservationConfig(context.Context, string) (core.ObservationConfig, error)
	SaveObservationConfig(context.Context, core.ObservationConfig, int64) error
	ListObservationConfigs(context.Context) ([]core.ObservationConfig, error)
	GetObservation(context.Context, string) (core.ApplicationObservation, error)
	SaveObservation(context.Context, core.ApplicationObservation, *core.ObservationEvent) error
	CreateObservationEvent(context.Context, core.ObservationEvent) error
	SaveObservationEvent(context.Context, core.ObservationEvent) error
	ListObservationEvents(context.Context, string) ([]core.ObservationEvent, error)
	PendingObservationEvents(context.Context, time.Time) ([]core.ObservationEvent, error)
}

var ErrBusy = errors.New("An observation or deployment is already in progress")
var ErrUnavailable = errors.New("Observation storage is unavailable")

type Service struct {
	Data       store.Store
	Repo       Repository
	Vault      *secretcrypto.Vault
	CheckDrift func(context.Context, string) (core.DriftCheck, error)
	Idle       func(context.Context, string, func() error) error
	Probe      func(context.Context, string) core.EndpointObservation
	Deliver    func(context.Context, string, core.ObservationEvent) error
	Now        func() time.Time
	mu         sync.Mutex
	busy       map[string]bool
	pending    map[string]core.Deployment
	wake       chan struct{}
}

func New(data store.Store, d *drift.Service, deployments *deploy.Service, vault *secretcrypto.Vault) *Service {
	repo, _ := data.(Repository)
	s := &Service{Data: data, Repo: repo, Vault: vault, Probe: ProbeEndpoint, Deliver: DeliverWebhook, Now: func() time.Time { return time.Now().UTC() }, busy: map[string]bool{}, pending: map[string]core.Deployment{}, wake: make(chan struct{}, 1)}
	if d != nil {
		s.CheckDrift = d.Check
	}
	if deployments != nil {
		s.Idle = deployments.WithIdleApplication
	}
	return s
}
func defaultConfig(app core.App) core.ObservationConfig {
	return core.ObservationConfig{AppID: app.ID, ProjectID: app.ProjectID, IntervalSeconds: 300, StaleAfterSeconds: 900}
}
func (s *Service) Config(ctx context.Context, appID string) (core.ObservationConfig, error) {
	app, err := s.Data.GetApp(ctx, appID)
	if err != nil {
		return core.ObservationConfig{}, err
	}
	if s.Repo == nil {
		return core.ObservationConfig{}, ErrUnavailable
	}
	c, err := s.Repo.GetObservationConfig(ctx, appID)
	if errors.Is(err, store.ErrNotFound) {
		return defaultConfig(app), nil
	}
	if err != nil {
		return c, err
	}
	if c.AppID != app.ID || c.ProjectID != app.ProjectID {
		return core.ObservationConfig{}, errors.New("Observation settings do not belong to this project")
	}
	return c, nil
}

type ConfigInput struct {
	Revision             int64      `json:"revision"`
	Scheduled            bool       `json:"scheduled"`
	IntervalSeconds      int        `json:"intervalSeconds"`
	StaleAfterSeconds    int        `json:"staleAfterSeconds"`
	EndpointURL          string     `json:"endpointUrl"`
	NotificationsEnabled bool       `json:"notificationsEnabled"`
	WebhookURL           *string    `json:"webhookUrl,omitempty"`
	RemoveWebhook        bool       `json:"removeWebhook,omitempty"`
	MutedUntil           *time.Time `json:"mutedUntil,omitempty"`
}

func (s *Service) Configure(ctx context.Context, app, actor string, input ConfigInput) (core.ObservationConfig, error) {
	c, err := s.Config(ctx, app)
	if err != nil {
		return c, err
	}
	if c.Revision != input.Revision {
		return c, store.ErrObservationConflict
	}
	if input.IntervalSeconds < 60 || input.IntervalSeconds > 86400 {
		return c, errors.New("Check interval must be between 60 seconds and 24 hours")
	}
	if input.StaleAfterSeconds < input.IntervalSeconds || input.StaleAfterSeconds > 7*86400 {
		return c, errors.New("Stale threshold must be at least the check interval and no more than seven days")
	}
	if input.EndpointURL != "" {
		if _, err = validateURL(input.EndpointURL, false); err != nil {
			return c, err
		}
	}
	now := s.Now()
	if input.MutedUntil != nil && (input.MutedUntil.Before(now) || input.MutedUntil.After(now.Add(30*24*time.Hour))) {
		return c, errors.New("Mute until must be in the next 30 days")
	}
	if input.RemoveWebhook && input.WebhookURL != nil {
		return c, errors.New("Choose either replacement or removal of the webhook")
	}
	if input.RemoveWebhook {
		c.WebhookCiphertext = ""
		c.WebhookConfigured = false
	}
	if input.WebhookURL != nil {
		if _, err = validateURL(*input.WebhookURL, true); err != nil {
			return c, err
		}
		if s.Vault == nil {
			return c, errors.New("Credential vault is unavailable")
		}
		c.WebhookCiphertext, err = s.Vault.Encrypt("observation-webhook:"+app, []byte(*input.WebhookURL))
		if err != nil {
			return c, errors.New("Could not protect the notification destination")
		}
		c.WebhookConfigured = true
	}
	if input.NotificationsEnabled && !c.WebhookConfigured {
		return c, errors.New("Configure a notification webhook before enabling delivery")
	}
	c.Scheduled = input.Scheduled
	c.IntervalSeconds = input.IntervalSeconds
	c.StaleAfterSeconds = input.StaleAfterSeconds
	c.EndpointURL = input.EndpointURL
	c.NotificationsEnabled = input.NotificationsEnabled
	c.MutedUntil = input.MutedUntil
	c.UpdatedAt = now
	c.UpdatedBy = actor
	c.Revision++
	err = s.Repo.SaveObservationConfig(ctx, c, input.Revision)
	if err != nil && !errors.Is(err, store.ErrObservationConflict) {
		return c, errors.New("Observation settings could not be saved")
	}
	return c, err
}

type Status struct {
	Configuration core.ObservationConfig      `json:"configuration"`
	Observation   core.ApplicationObservation `json:"observation"`
	Freshness     string                      `json:"freshness"`
	Checking      bool                        `json:"checking"`
	Events        []core.ObservationEvent     `json:"events"`
}

func (s *Service) Status(ctx context.Context, app string) (Status, error) {
	c, err := s.Config(ctx, app)
	if err != nil {
		return Status{}, err
	}
	out := Status{Configuration: c, Freshness: "not_checked", Events: []core.ObservationEvent{}, Observation: core.ApplicationObservation{AppID: app, ProjectID: c.ProjectID, Location: "Dispatch controller", State: "not_checked", Endpoint: core.EndpointObservation{State: "not_configured", TLS: "not_checked", Location: "Dispatch controller"}}}
	old, err := s.Repo.GetObservation(ctx, app)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return out, err
	}
	if err == nil && old.ProjectID == c.ProjectID && old.ConfigurationRevision == c.Revision {
		out.Observation = old
		if old.CheckedAt != nil {
			out.Freshness = "fresh"
			if s.Now().Sub(*old.CheckedAt) > time.Duration(c.StaleAfterSeconds)*time.Second {
				out.Freshness = "stale"
			}
		}
	}
	s.mu.Lock()
	out.Checking = s.busy[app]
	s.mu.Unlock()
	if out.Checking {
		out.Freshness = "checking"
	}
	out.Events, err = s.Repo.ListObservationEvents(ctx, app)
	if err != nil {
		return out, err
	}
	for _, e := range out.Events {
		if e.ProjectID != c.ProjectID {
			return Status{}, errors.New("Observation history does not belong to this project")
		}
	}
	return out, nil
}
func (s *Service) claim(app string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[app] {
		return false
	}
	s.busy[app] = true
	return true
}
func (s *Service) release(app string) { s.mu.Lock(); delete(s.busy, app); s.mu.Unlock() }
func (s *Service) Check(ctx context.Context, app, source string) (core.ApplicationObservation, error) {
	if !s.claim(app) {
		return core.ApplicationObservation{}, ErrBusy
	}
	defer s.release(app)
	return s.check(ctx, app, source)
}
func (s *Service) check(ctx context.Context, appID, source string) (core.ApplicationObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	c, err := s.Config(ctx, appID)
	if err != nil {
		return core.ApplicationObservation{}, err
	}
	app, err := s.Data.GetApp(ctx, appID)
	if err != nil {
		return core.ApplicationObservation{}, err
	}
	if source == "schedule" && !c.Scheduled {
		return core.ApplicationObservation{}, nil
	}
	old, e := s.Repo.GetObservation(ctx, appID)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return core.ApplicationObservation{}, e
	}
	if old.ProjectID != c.ProjectID || old.ConfigurationRevision != c.Revision {
		old = core.ApplicationObservation{}
	}
	result := core.ApplicationObservation{AppID: appID, ProjectID: c.ProjectID, ConfigurationRevision: c.Revision, Source: source, Drift: "not_supported", Health: "not_supported", Location: "Dispatch controller"}
	perform := func() error {
		latest, latestErr := s.Data.LatestSuccessfulDeployment(ctx, appID)
		if latestErr == nil {
			result.DeploymentID = latest.ID
		} else if !errors.Is(latestErr, store.ErrNotFound) {
			return latestErr
		}
		server, e := s.Data.GetServer(ctx, app.ServerID)
		if e != nil {
			return e
		}
		if app.BuildType == core.BuildTypeHelm && server.Kubernetes != nil && !app.Template && s.CheckDrift != nil {
			checked, e := s.CheckDrift(ctx, appID)
			if e != nil {
				return errors.New("The runtime observation could not be saved")
			}
			result.Drift = checked.State
			result.Health = checked.Health
			result.DeploymentID = checked.DeploymentID
			result.Message = checked.Message
		}
		result.Endpoint = s.Probe(ctx, c.EndpointURL)
		return nil
	}
	if s.Idle != nil {
		err = s.Idle(ctx, appID, perform)
	} else {
		err = perform()
	}
	if errors.Is(err, deploy.ErrDeploymentActive) {
		return result, ErrBusy
	}
	if err != nil {
		return result, err
	}
	now := s.Now()
	result.CheckedAt = &now
	result.State = aggregate(result)
	if result.State == "unknown" || result.State == "unhealthy" {
		result.ConsecutiveFailures = old.ConsecutiveFailures + 1
	}
	if c.Scheduled {
		next := now.Add(backoff(c.IntervalSeconds, result.ConsecutiveFailures))
		result.NextCheckAt = &next
	}
	// A settings edit during network IO invalidates this observation and its event.
	current, e := s.Config(ctx, appID)
	if e != nil {
		return result, e
	}
	if current.Revision != c.Revision {
		return result, store.ErrObservationConflict
	}
	var event *core.ObservationEvent
	if old.State != result.State && (old.CheckedAt != nil || result.State == "unhealthy" || result.State == "unknown") {
		kind := "state_changed"
		if result.State == "healthy" && (old.State == "unhealthy" || old.State == "unknown") {
			kind = "recovered"
		}
		value := s.event(c, result.DeploymentID, kind, old.State, result.State, observationMessage(result), now)
		event = &value
	}
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	return result, s.Repo.SaveObservation(saveCtx, result, event)
}
func observationMessage(o core.ApplicationObservation) string {
	switch {
	case o.Drift == "out_of_sync":
		return "Runtime differs from the successful deployment. Open drift details to review the changed resources."
	case o.Health == "degraded":
		return "Workload readiness is degraded. Open the deployment to inspect failing resources and events."
	case o.Endpoint.State != "reachable" && o.Endpoint.State != "not_configured":
		return "The public endpoint check failed. Review its HTTP and TLS result in deployment details."
	case o.State == "unknown":
		return "Runtime observation is incomplete. Review resource access, target connectivity and the saved release baseline."
	case o.State == "healthy":
		return "Application observations are healthy."
	default:
		return "Application observation is " + o.State + "."
	}
}

func aggregate(o core.ApplicationObservation) string {
	if o.Health == "degraded" || o.Drift == "out_of_sync" || (o.Endpoint.State != "reachable" && o.Endpoint.State != "not_configured") {
		return "unhealthy"
	}
	if o.Health == "unknown" || o.Drift == "unknown" {
		return "unknown"
	}
	if o.Health == "progressing" {
		return "progressing"
	}
	if o.Health == "not_supported" && o.Drift == "not_supported" && o.Endpoint.State == "not_configured" {
		return "not_supported"
	}
	return "healthy"
}
func backoff(interval, failures int) time.Duration {
	if interval < 60 {
		interval = 60
	}
	delay := time.Duration(interval) * time.Second
	// First failure uses the chosen interval. Retry delay is capped at 24h.
	for n := 1; n < failures && delay < 24*time.Hour; n++ {
		delay *= 2
	}
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	return delay
}
func (s *Service) event(c core.ObservationConfig, deployment, kind, previous, state, message string, now time.Time) core.ObservationEvent {
	e := core.ObservationEvent{ID: ulid.Make().String(), AppID: c.AppID, ProjectID: c.ProjectID, ConfigurationRevision: c.Revision, DeploymentID: deployment, Kind: kind, PreviousState: previous, State: state, Message: message, Link: "/applications", CreatedAt: now, Delivery: "disabled"}
	if deployment != "" {
		e.Link = "/deployments/" + url.PathEscape(deployment)
	}
	if c.NotificationsEnabled && c.WebhookConfigured {
		e.Delivery = "pending"
		e.NextAttemptAt = &now
		if c.MutedUntil != nil && c.MutedUntil.After(now) {
			e.Delivery = "muted"
			e.NextAttemptAt = nil
		}
	}
	return e
}

// DeploymentFinished only enqueues work and is safe to call while the deployer
// holds its operation lock. The worker reacquires the idle-application guard.
func (s *Service) DeploymentFinished(d core.Deployment) {
	if d.State != core.DeploymentSucceeded && d.State != core.DeploymentFailed {
		return
	}
	s.mu.Lock()
	s.pending[d.ID] = d
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) completed(ctx context.Context, d core.Deployment) error {
	c, err := s.Config(ctx, d.AppID)
	if err != nil {
		return err
	}
	app, err := s.Data.GetApp(ctx, d.AppID)
	if err != nil || app.ProjectID != c.ProjectID {
		return errors.New("Application project changed")
	}
	// IDs make duplicate finish callbacks and controller restarts idempotent.
	now := d.CreatedAt
	if d.FinishedAt != nil {
		now = *d.FinishedAt
	}
	if now.IsZero() {
		now = s.Now()
	}
	kind, state, message := "deployment_succeeded", "healthy", "Deployment succeeded."
	if d.State == core.DeploymentFailed {
		kind, state, message = "deployment_failed", "unhealthy", "Deployment failed. Open the deployment for logs and diagnostics."
	}
	e := s.event(c, d.ID, kind, "", state, message, now)
	e.ID = "deployment:" + d.ID
	if d.State == core.DeploymentSucceeded {
		events, err := s.Repo.ListObservationEvents(ctx, d.AppID)
		if err != nil {
			return err
		}
		recovered := false
		for _, previous := range events {
			if !previous.CreatedAt.Before(now) {
				continue
			}
			if previous.Kind == "deployment_failed" || previous.Kind == "deployment_succeeded" || previous.Kind == "deployment_recovered" {
				recovered = previous.Kind == "deployment_failed"
				break
			}
		}
		if recovered {
			e.Kind = "deployment_recovered"
			e.Message = "Deployment recovered after the previous failure."
		} else {
			e.Delivery = "disabled"
			e.NextAttemptAt = nil
		}
	}
	if err = s.Repo.CreateObservationEvent(ctx, e); err != nil {
		return err
	}
	if d.State == core.DeploymentSucceeded {
		latest, e := s.Data.LatestSuccessfulDeployment(ctx, d.AppID)
		if e != nil {
			return e
		}
		if latest.ID != d.ID {
			return nil
		}
		_, err = s.check(ctx, d.AppID, "deployment")
	}
	return err
}

type task struct {
	app, source string
	deployment  *core.Deployment
}

func (s *Service) Run(ctx context.Context) {
	if s.Repo == nil {
		return
	}
	tasks := make(chan task, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case work := <-tasks:
					var err error
					if work.deployment != nil {
						err = s.completed(ctx, *work.deployment)
					} else {
						_, err = s.check(ctx, work.app, work.source)
					}
					s.release(work.app)
					if err != nil && ctx.Err() == nil && work.deployment != nil {
						s.mu.Lock()
						if _, ok := s.pending[work.deployment.ID]; !ok {
							s.pending[work.deployment.ID] = *work.deployment
						}
						s.mu.Unlock()
					}
					if err == nil {
						s.mu.Lock()
						waiting := len(s.pending) > 0
						s.mu.Unlock()
						if waiting {
							select {
							case s.wake <- struct{}{}:
							default:
							}
						}
					}
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.DeliverPending(ctx)
			}
		}
	}()
	// Recover finish callbacks missed by a controller restart. Only a deployment
	// without an observation of its successful revision is checked automatically.
	if apps, err := s.Data.ListActiveApps(ctx); err == nil {
		for _, app := range apps {
			// A failed attempt can be persisted immediately before a controller stops.
			// Its stable event ID prevents notifying again on subsequent restarts.
			if attempts, e := s.Data.ListApplicationHistory(ctx, app.ID, "", 1); e == nil && len(attempts) > 0 && attempts[0].State == core.DeploymentFailed {
				s.DeploymentFinished(attempts[0])
			}
			d, e := s.Data.LatestSuccessfulDeployment(ctx, app.ID)
			if e != nil {
				continue
			}
			old, e := s.Repo.GetObservation(ctx, app.ID)
			if e != nil || old.DeploymentID != d.ID {
				s.DeploymentFinished(d)
			}
		}
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	submit := func(t task) {
		if !s.claim(t.app) {
			return
		}
		select {
		case tasks <- t:
		case <-ctx.Done():
			s.release(t.app)
		}
	}
	scan := func() {
		s.mu.Lock()
		pending := s.pending
		s.pending = map[string]core.Deployment{}
		s.mu.Unlock()
		ordered := make([]core.Deployment, 0, len(pending))
		for _, d := range pending {
			ordered = append(ordered, d)
		}
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].CreatedAt.Before(ordered[j].CreatedAt) })
		for _, d := range ordered {
			if ctx.Err() != nil {
				return
			}
			s.mu.Lock()
			busy := s.busy[d.AppID]
			if busy {
				s.pending[d.ID] = d
			}
			s.mu.Unlock()
			if !busy {
				copy := d
				submit(task{app: d.AppID, source: "deployment", deployment: &copy})
			}
		}
		configs, err := s.Repo.ListObservationConfigs(ctx)
		if err != nil {
			return
		}
		now := s.Now()
		for _, c := range configs {
			if !c.Scheduled {
				continue
			}
			app, e := s.Data.GetApp(ctx, c.AppID)
			if e != nil || app.ProjectID != c.ProjectID || app.Template {
				continue
			}
			old, e := s.Repo.GetObservation(ctx, c.AppID)
			if e == nil && old.ConfigurationRevision == c.Revision && old.NextCheckAt != nil && old.NextCheckAt.After(now) {
				continue
			}
			submit(task{app: c.AppID, source: "schedule"})
		}
	}
	scan()
	for {
		select {
		case <-ctx.Done():
			workers.Wait()
			return
		case <-ticker.C:
			scan()
		case <-s.wake:
			scan()
		}
	}
}
func (s *Service) DeliverPending(ctx context.Context) error {
	events, err := s.Repo.PendingObservationEvents(ctx, s.Now())
	if err != nil {
		return err
	}
	for _, e := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, err := s.Config(ctx, e.AppID)
		if err != nil || c.ProjectID != e.ProjectID || c.Revision != e.ConfigurationRevision || !c.NotificationsEnabled || !c.WebhookConfigured {
			e.Delivery = "cancelled"
			e.DeliveryMessage = "Delivery configuration changed."
		} else if c.MutedUntil != nil && c.MutedUntil.After(s.Now()) {
			e.Delivery = "muted"
			e.DeliveryMessage = "Notifications are muted."
		} else {
			e.Attempts++
			var raw []byte
			if s.Vault == nil {
				err = errors.New("Vault unavailable")
			} else {
				raw, err = s.Vault.Decrypt("observation-webhook:"+e.AppID, c.WebhookCiphertext)
			}
			if err == nil {
				deliveryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err = s.Deliver(deliveryCtx, string(raw), e)
				cancel()
			}
			now := s.Now()
			if err == nil {
				e.Delivery = "delivered"
				e.DeliveredAt = &now
				e.DeliveryMessage = "Delivered."
			} else {
				e.DeliveryMessage = "Delivery failed; destination or network unavailable."
				if e.Attempts >= 5 {
					e.Delivery = "failed"
				} else {
					next := now.Add(time.Minute * time.Duration(1<<uint(e.Attempts-1)))
					e.NextAttemptAt = &next
				}
			}
		}
		if e.Delivery != "pending" {
			e.NextAttemptAt = nil
		}
		if err = s.Repo.SaveObservationEvent(ctx, e); err != nil {
			return fmt.Errorf("save notification outcome: %w", err)
		}
	}
	return nil
}
