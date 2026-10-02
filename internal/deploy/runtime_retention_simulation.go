package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// SimulationRetention models revision retirement without issuing runtime commands.
// It uses the same controller reference fences and reviewed policy as Docker.
type SimulationRetention struct{ Store store.Store }

func (s SimulationRetention) InspectRetention(ctx context.Context, app core.App, server core.Server) ([]core.RuntimeRetentionItem, error) {
	data, ok := s.Store.(interface {
		RuntimeRetentionReferences(context.Context, string, int) ([]core.RuntimeRetentionReference, error)
		RetiredRuntimeDeployments(context.Context, string) (map[string]bool, error)
	})
	if !ok {
		return nil, errors.New("Simulation retention inventory is unavailable")
	}
	refs, err := data.RuntimeRetentionReferences(ctx, app.ID, 0)
	if err != nil {
		return nil, err
	}
	retired, err := data.RetiredRuntimeDeployments(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	items := []core.RuntimeRetentionItem{}
	for _, ref := range refs {
		if retired[ref.DeploymentID] {
			continue
		}
		d, err := s.Store.GetDeployment(ctx, ref.DeploymentID)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(d.Snapshot)
		items = append(items, core.RuntimeRetentionItem{Key: "revision:" + server.ID + ":" + d.ID, Kind: "revision", ServerID: server.ID, AppID: app.ID, DeploymentID: d.ID, Name: "Simulated revision " + d.ID, Identity: fmt.Sprintf("%x", sha256.Sum256(raw)), CreatedAt: d.CreatedAt, Protected: []string{}})
	}
	return items, nil
}
func (s SimulationRetention) PruneRetention(ctx context.Context, app core.App, server core.Server, item core.RuntimeRetentionItem) (core.RuntimeRetentionOutcome, error) {
	out := core.RuntimeRetentionOutcome{Key: item.Key, State: "absent", Message: "Simulated revision is already retired"}
	items, err := s.InspectRetention(ctx, app, server)
	if err != nil {
		return out, err
	}
	for _, current := range items {
		if current.Key == item.Key {
			if item.Kind != "revision" || item.Identity != current.Identity || len(item.Protected) > 0 {
				return out, errors.New("Simulation revision changed after review")
			}
			out.State, out.Message = "removed", "Retired simulated revision inputs"
			return out, nil
		}
	}
	return out, nil
}
