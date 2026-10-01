package provision

import (
	"context"
	"encoding/json"

	"github.com/doout/dispatch/internal/core"
)

type CatalogProvider struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Capabilities []string        `json:"capabilities"`
	Manifest     json.RawMessage `json:"manifest"`
}

func (m *Manager) Catalog(ctx context.Context, project string) ([]CatalogProvider, error) {
	data, err := m.lifecycle()
	if err != nil {
		return nil, err
	}
	if _, err = data.GetProject(ctx, project); err != nil {
		return nil, err
	}
	items, err := data.ListInfrastructureProviders(ctx)
	if err != nil {
		return nil, err
	}
	result := []CatalogProvider{}
	for _, p := range items {
		if !p.Enabled || p.State != "ready" || m.authorize(ctx, project, p.ID, "infrastructure.inspect") != nil {
			continue
		}
		result = append(result, CatalogProvider{ID: p.ID, Name: p.Name, Capabilities: p.Capabilities, Manifest: p.Manifest})
	}
	return result, nil
}
func (m *Manager) ListManaged(ctx context.Context) ([]core.ManagedServer, error) {
	data, err := m.lifecycle()
	if err != nil {
		return nil, err
	}
	items, err := data.ListManagedServers(ctx)
	if err != nil {
		return nil, err
	}
	result := []core.ManagedServer{}
	for _, item := range items {
		if m.authorize(ctx, item.ProjectID, item.ProviderID, "infrastructure.inspect") == nil {
			result = append(result, item)
		}
	}
	return result, nil
}
func (m *Manager) Operations(ctx context.Context, id string) ([]core.InfrastructureOperation, error) {
	data, err := m.lifecycle()
	if err != nil {
		return nil, err
	}
	server, err := data.GetManagedServer(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = m.authorize(ctx, server.ProjectID, server.ProviderID, "infrastructure.inspect"); err != nil {
		return nil, err
	}
	return data.ListInfrastructureOperations(ctx, id)
}
