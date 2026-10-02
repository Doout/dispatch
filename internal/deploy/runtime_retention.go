package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type RuntimeRetentionStore interface {
	GetRetentionPolicy(context.Context, string) (core.RetentionPolicy, error)
	RuntimeRetentionReferences(context.Context, string, int) ([]core.RuntimeRetentionReference, error)
	SaveRuntimeRetentionReview(context.Context, core.RuntimeRetentionReview) error
	GetRuntimeRetentionReview(context.Context, string) (core.RuntimeRetentionReview, error)
	ClaimRuntimeRetentionReview(context.Context, core.RuntimeRetentionReview, time.Time) error
	UpdateRuntimeRetentionReview(context.Context, core.RuntimeRetentionReview, bool) error
	BeginRuntimeArtifactRetirement(context.Context, core.RuntimeRetentionReview, core.RuntimeRetentionItem) error
	FinishRuntimeArtifactRetirement(context.Context, string, string) error
}

func (s *Service) runtimeRetentionStore() (RuntimeRetentionStore, error) {
	data, ok := s.store.(RuntimeRetentionStore)
	if !ok || s.Retention == nil {
		return nil, errors.New("Runtime artifact retention is unavailable")
	}
	return data, nil
}
func retentionBinding(server core.Server) string {
	raw, _ := json.Marshal([]string{server.ID, server.AgentNodeID, server.Address, string(server.Runtime)})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func (s *Service) PreviewRuntimeRetention(ctx context.Context, p core.RetentionPolicy) (core.RuntimeRetentionReview, error) {
	data, err := s.runtimeRetentionStore()
	if err != nil {
		return core.RuntimeRetentionReview{}, err
	}
	saved, err := data.GetRetentionPolicy(ctx, p.ProjectID)
	if err != nil {
		return core.RuntimeRetentionReview{}, err
	}
	if saved != p {
		return core.RuntimeRetentionReview{}, store.ErrRetentionPolicyChanged
	}
	now := time.Now().UTC()
	r := core.RuntimeRetentionReview{ID: ulid.Make().String(), ProjectID: p.ProjectID, Policy: p, State: "planned", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), Items: []core.RuntimeRetentionItem{}, Results: []core.RuntimeRetentionOutcome{}}
	apps, err := s.store.ListAppsForUsage(ctx)
	if err != nil {
		return r, err
	}
	seen := map[string]bool{}
	eligible := 0
	for _, app := range apps {
		if app.ProjectID != p.ProjectID || app.Template {
			continue
		}
		server, err := s.store.GetServer(ctx, app.ServerID)
		if err != nil {
			return r, err
		}
		var items []core.RuntimeRetentionItem
		err = s.Storage.WithTarget(ctx, server.ID, func() error {
			var inspectErr error
			items, inspectErr = s.Retention.InspectRetention(ctx, app, server)
			return inspectErr
		})
		if err != nil {
			r.Items = append(r.Items, core.RuntimeRetentionItem{Key: "unavailable:" + app.ID, Kind: "revision", ServerID: server.ID, AppID: app.ID, Name: app.Name, Protected: []string{"Runtime inventory is unavailable; no cleanup is authorized"}})
			continue
		}
		refs, err := data.RuntimeRetentionReferences(ctx, app.ID, p.RollbackRetentionCount())
		if err != nil {
			return r, err
		}
		references := map[string]core.RuntimeRetentionReference{}
		for _, ref := range refs {
			references[ref.DeploymentID] = ref
		}
		for _, item := range items {
			if item.AppID != app.ID || item.ServerID != server.ID {
				return r, errors.New("Runtime inventory ownership does not match the application")
			}
			if seen[item.Key] {
				continue
			}
			seen[item.Key] = true
			item.TargetBinding = retentionBinding(server)
			if item.Kind == "revision" {
				ref, ok := references[item.DeploymentID]
				if !ok {
					item.Protected = appendUnique(item.Protected, "Deployment history is unavailable")
				} else {
					item.CreatedAt = ref.CreatedAt
					item.Protected = append(item.Protected, ref.Protected...)
				}
			}
			days := p.ImageDays
			if item.Kind == "revision" {
				days = p.StoppedRevisionDays
			}
			if days == 0 {
				item.Protected = appendUnique(item.Protected, "Policy preserves these artifacts indefinitely")
			} else if item.CreatedAt.IsZero() || !item.CreatedAt.Before(now.AddDate(0, 0, -days)) {
				item.Protected = appendUnique(item.Protected, "Artifact is younger than the retention age or its age is unknown")
			}
			if len(item.Protected) == 0 {
				if eligible >= 50 {
					item.Protected = append(item.Protected, "Review limit reached; create another review after cleanup")
				} else {
					eligible++
				}
			}
			r.Items = append(r.Items, item)
			if len(r.Items) > 1000 {
				return r, errors.New("Project inventory exceeds the retention review safety limit")
			}
		}
	}
	sort.Slice(r.Items, func(i, j int) bool { return r.Items[i].Key < r.Items[j].Key })
	raw, _ := json.Marshal(r)
	r.Digest = fmt.Sprintf("%x", sha256.Sum256(raw))
	return r, data.SaveRuntimeRetentionReview(ctx, r)
}
func (s *Service) GetRuntimeRetentionReview(ctx context.Context, project, id string) (core.RuntimeRetentionReview, error) {
	data, err := s.runtimeRetentionStore()
	if err != nil {
		return core.RuntimeRetentionReview{}, err
	}
	r, err := data.GetRuntimeRetentionReview(ctx, id)
	if err == nil && r.ProjectID != project {
		return core.RuntimeRetentionReview{}, store.ErrNotFound
	}
	return r, err
}
func (s *Service) ApplyRuntimeRetention(ctx context.Context, p core.RetentionPolicy, id, digest string) (core.RuntimeRetentionReview, error) {
	data, err := s.runtimeRetentionStore()
	if err != nil {
		return core.RuntimeRetentionReview{}, err
	}
	r, err := s.GetRuntimeRetentionReview(ctx, p.ProjectID, id)
	if err != nil {
		return r, err
	}
	if r.Digest != digest || r.Policy != p {
		return r, store.ErrRuntimeRetentionChanged
	}
	if r.State == "succeeded" {
		return r, nil
	}
	r.Attempt = ulid.Make().String()
	if err = data.ClaimRuntimeRetentionReview(ctx, r, time.Now().UTC()); err != nil {
		return r, err
	}
	r.State = "running"
	// The durable review bounds both the original attempt and every retry. No fresh
	// inventory can add a deletion candidate to this accepted set.
	ctx = context.WithValue(ctx, retentionReviewContextKey{}, r)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	results := map[string]core.RuntimeRetentionOutcome{}
	for _, result := range r.Results {
		results[result.Key] = result
	}
	for _, item := range r.Items {
		if len(item.Protected) > 0 {
			continue
		}
		if previous := results[item.Key]; previous.State == "removed" || previous.State == "absent" {
			continue
		}
		outcome := core.RuntimeRetentionOutcome{Key: item.Key, State: "failed", Message: "Runtime cleanup could not complete; retry this review"}
		// Inspection may reconcile a previous unknown agent cleanup before the
		// mutation lock is checked. It never broadens the reviewed deletion set.
		preflightApp, preflightErr := s.store.GetApp(ctx, item.AppID)
		if preflightErr == nil && (preflightApp.ProjectID != r.ProjectID || preflightApp.ServerID != item.ServerID) {
			preflightErr = store.ErrRuntimeRetentionChanged
		}
		if preflightErr == nil {
			var target core.Server
			target, preflightErr = s.store.GetServer(ctx, item.ServerID)
			if preflightErr == nil && retentionBinding(target) != item.TargetBinding {
				preflightErr = store.ErrRuntimeRetentionChanged
			}
			if preflightErr == nil {
				_, preflightErr = s.Retention.InspectRetention(ctx, preflightApp, target)
			}
		}
		if preflightErr != nil {
			outcome.Message = "Runtime inspection is unavailable; retry the original review"
			if errors.Is(preflightErr, store.ErrRuntimeRetentionChanged) {
				outcome.State, outcome.Message = "protected", "Application ownership or target changed after review"
			}
			results[item.Key] = outcome
			continue
		}
		err = s.WithIdleApplication(ctx, item.AppID, func() error {
			app, err := s.store.GetApp(ctx, item.AppID)
			if err != nil {
				return err
			}
			if app.ProjectID != r.ProjectID || app.ServerID != item.ServerID {
				return store.ErrRuntimeRetentionChanged
			}
			server, err := s.store.GetServer(ctx, item.ServerID)
			if err != nil {
				return err
			}
			if item.TargetBinding != retentionBinding(server) {
				return store.ErrRuntimeRetentionChanged
			}
			return s.Storage.WithTarget(ctx, server.ID, func() error {
				current, err := data.GetRetentionPolicy(ctx, r.ProjectID)
				if err != nil {
					return err
				}
				if current != p {
					return store.ErrRetentionPolicyChanged
				}
				if item.Kind == "revision" {
					if err = data.BeginRuntimeArtifactRetirement(ctx, r, item); err != nil {
						return err
					}
				}
				outcome, err = s.Retention.PruneRetention(ctx, app, server, item)
				if err != nil {
					return err
				}
				if item.Kind == "revision" && (outcome.State == "removed" || outcome.State == "absent") {
					return data.FinishRuntimeArtifactRetirement(ctx, r.ID, item.DeploymentID)
				}
				return nil
			})
		})
		if errors.Is(err, store.ErrRuntimeRetentionChanged) || errors.Is(err, store.ErrRetentionPolicyChanged) || errors.Is(err, ErrDeploymentActive) {
			outcome.State, outcome.Message = "protected", "Policy, target, or deployment references changed after review"
		}
		if err != nil && outcome.State == "removed" {
			outcome.State, outcome.Message = "failed", "Runtime artifact was removed; retry to finish its retained reference receipt"
		}
		outcome.Key = item.Key
		results[item.Key] = outcome
		r.Results = retentionOutcomes(r.Items, results)
		if saveErr := data.UpdateRuntimeRetentionReview(context.WithoutCancel(ctx), r, false); saveErr != nil {
			return r, saveErr
		}
		if ctx.Err() != nil {
			break
		}
	}
	r.State = "succeeded"
	for _, item := range r.Items {
		if len(item.Protected) > 0 {
			continue
		}
		outcome := results[item.Key]
		if outcome.State != "removed" && outcome.State != "absent" {
			r.State = "partial"
			break
		}
	}
	r.Results = retentionOutcomes(r.Items, results)
	return r, data.UpdateRuntimeRetentionReview(context.WithoutCancel(ctx), r, true)
}
func retentionOutcomes(items []core.RuntimeRetentionItem, results map[string]core.RuntimeRetentionOutcome) []core.RuntimeRetentionOutcome {
	out := []core.RuntimeRetentionOutcome{}
	for _, item := range items {
		if result, ok := results[item.Key]; ok {
			out = append(out, result)
		}
	}
	return out
}
