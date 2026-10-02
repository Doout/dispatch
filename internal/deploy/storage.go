package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// StorageBackend keeps runtime evidence separate from operator policy. Remote
// drivers must supply equivalent inspection before enabling destructive work.
type StorageBackend interface {
	Inspect(context.Context, core.Server) ([]core.StorageObservation, error)
	Delete(context.Context, core.Server, core.StorageResource) error
}

type StorageManager struct {
	Store   store.Store
	Backend StorageBackend
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
}

func NewStorageManager(data store.Store) *StorageManager {
	return &StorageManager{Store: data, locks: map[string]*sync.Mutex{}}
}

// WithTarget serializes policy, inspection and runtime mutation in this single
// active controller. Runtime UID checks and database revisions also reject stale work.
func (m *StorageManager) WithTarget(ctx context.Context, id string, fn func() error) error {
	m.mu.Lock()
	lock := m.locks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		m.locks[id] = lock
	}
	m.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (m *StorageManager) Refresh(ctx context.Context, server core.Server) error {
	return m.WithTarget(ctx, server.ID, func() error { return m.RefreshLocked(ctx, server) })
}

// Namespace removal cascades to PVCs even when Helm retained them. Preview
// cleanup must leave that namespace until data is explicitly removed or moved.
func (m *StorageManager) CleanupNamespace(ctx context.Context, server core.Server, namespace string, cleanup func() error) error {
	return m.WithTarget(ctx, server.ID, func() error {
		// The requested preview may no longer have an application record.
		// Always inspect its namespace before a cascading namespace deletion.
		if core.IsKubernetesRuntime(server.Runtime) && server.Kubernetes != nil {
			config := *server.Kubernetes
			config.Namespace = namespace
			server.Kubernetes = &config
		}
		if err := m.RefreshLocked(ctx, server); err != nil {
			return err
		}
		items, err := m.Store.ListStorage(ctx, server.ID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if item.Namespace == namespace && item.State != "absent" {
				return errors.New("preview namespace contains protected storage; retain the namespace until data is explicitly removed or migrated")
			}
		}
		return cleanup()
	})
}

func (m *StorageManager) CheckApplicationCleanup(ctx context.Context, app core.App, server core.Server) error {
	if app.BuildType != core.BuildTypeHelm {
		return nil
	}
	items, err := m.Store.ListStorage(ctx, server.ID)
	if err != nil {
		return err
	}
	for _, item := range items {
		// Unverified claims in a shared namespace can belong to unrelated pods.
		// Helm checks their live owner chains against its actual release manifest.
		if item.State == "absent" || item.Namespace != helmNamespace(app, server) || item.OwnerID != app.ID || item.Ownership != "verified" {
			continue
		}
		for _, consumer := range item.Consumers {
			if strings.HasPrefix(consumer.ID, "owner:") {
				return errors.New("storage has live garbage-collection owner references; set its workload data retention to Retain and reconcile before cleanup")
			}
		}
	}
	return nil
}

func (m *StorageManager) RefreshLocked(ctx context.Context, server core.Server) error {
	if m.Backend == nil {
		return nil
	} // Simulation has no runtime storage to inspect.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	previous, err := m.Store.ListStorage(ctx, server.ID)
	if err != nil {
		return err
	}
	apps, err := m.Store.ListAppsForUsage(ctx)
	if err != nil {
		return err
	}
	services, err := m.Store.ListServices(ctx, "")
	if err != nil {
		return err
	}
	runs, err := m.Store.ListServiceProvisionRuns(ctx, "")
	if err != nil {
		return err
	}
	observed, err := m.inspectTarget(ctx, server, apps, runs, previous)
	if err != nil {
		statusCtx, statusCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer statusCancel()
		for _, item := range previous {
			if item.State == "absent" {
				continue
			}
			item.State, item.Message = "inaccessible", "Storage inspection failed; retained data has not been declared absent."
			_ = m.Store.ObserveStorage(statusCtx, item)
		}
		return errors.New("cannot inspect target storage; cleanup is blocked until inspection succeeds")
	}
	old := map[string]core.StorageResource{}
	for _, item := range previous {
		old[item.ID] = item
	}
	seen := map[string]bool{}
	for _, observation := range observed {
		item := observation.Resource
		item.ProjectID, item.OwnerKind, item.OwnerID, item.ProvisionRunID, item.Orphaned = "", "", "", "", false
		item.ServerID, item.ID = server.ID, StorageID(server.ID, item.Kind, item.Namespace, item.Name)
		if item.State == "" {
			item.State = "present"
		}
		item.Ownership, item.ObservedAt = "unverified", time.Now().UTC()
		item.Consumers = append([]core.StorageConsumer{}, item.Consumers...)
		sort.Slice(item.Consumers, func(i, j int) bool {
			if item.Consumers[i].ID == item.Consumers[j].ID {
				return item.Consumers[i].Mount < item.Consumers[j].Mount
			}
			return item.Consumers[i].ID < item.Consumers[j].ID
		})
		labels := observation.Labels
		// Only verified runtime labels/Helm annotations can select an owner.
		for _, app := range apps {
			if app.ServerID != server.ID {
				continue
			}
			owned := labels["dispatch.app"] == app.ID || labels["dispatch.app/app-id"] == app.ID && labels["dispatch.app/managed-by"] == "dispatch"
			if app.BuildType == core.BuildTypeCompose {
				owned = owned || labels["com.docker.compose.project"] == dockerResourceName(app.ID)
			}
			if app.BuildType == core.BuildTypeHelm {
				owned = owned || labels["meta.helm.sh/release-name"] == helmReleaseName(app) && labels["meta.helm.sh/release-namespace"] == helmNamespace(app, server) && labels["app.kubernetes.io/managed-by"] == "Helm"
			}
			if owned && (labels["dispatch.project"] == "" || labels["dispatch.project"] == app.ProjectID) {
				if item.OwnerID != "" {
					item.Ownership = "conflict"
					break
				}
				item.ProjectID, item.OwnerKind, item.OwnerID, item.Ownership = app.ProjectID, "application", app.ID, "verified"
				item.Orphaned = app.State == "closed"
			}
		}
		for _, run := range runs {
			if run.Target == nil || run.Target.ServerID != server.ID {
				continue
			}
			owned := labels["dispatch.service-provision"] == run.ID && labels["dispatch.project"] == run.ProjectID && labels["dispatch.managed-by"] == "dispatch"
			owned = owned || labels["dispatch.app/app-id"] == run.ID && labels["dispatch.app/managed-by"] == "dispatch" && item.Namespace == run.Target.Namespace
			if run.Target.Provider == "helm" {
				owned = owned || labels["meta.helm.sh/release-name"] == run.Target.ResourceName && labels["meta.helm.sh/release-namespace"] == run.Target.Namespace && labels["app.kubernetes.io/managed-by"] == "Helm"
			}
			if !owned || labels["dispatch.project"] != "" && labels["dispatch.project"] != run.ProjectID {
				continue
			}
			if item.OwnerID != "" {
				item.Ownership = "conflict"
				break
			}
			item.ProjectID, item.OwnerKind, item.OwnerID, item.ProvisionRunID, item.Ownership = run.ProjectID, "provision-run", run.ID, run.ID, "verified"
			for _, service := range services {
				if service.ProvisionRunID == run.ID && service.ProjectID == run.ProjectID {
					item.OwnerKind, item.OwnerID = "service", service.ID
					break
				}
			}
		}
		// Deleted workload records do not discard verified provenance. Changing
		// runtime labels or reusing a storage name invalidates that provenance.
		prior, exists := old[item.ID]
		mounts := map[string]bool{}
		if exists && prior.Identity == item.Identity {
			for _, mount := range prior.Mounts {
				mounts[mount] = true
			}
		}
		for _, consumer := range item.Consumers {
			if consumer.Mount != "" {
				mounts[consumer.Mount] = true
			}
		}
		item.Mounts = []string{}
		for mount := range mounts {
			item.Mounts = append(item.Mounts, mount)
		}
		sort.Strings(item.Mounts)
		if item.OwnerID == "" && exists && prior.Ownership == "verified" && prior.Identity == item.Identity && prior.Evidence == item.Evidence {
			item.ProjectID, item.OwnerKind, item.OwnerID, item.ProvisionRunID, item.Ownership, item.Orphaned = prior.ProjectID, prior.OwnerKind, prior.OwnerID, prior.ProvisionRunID, "verified", true
		}
		if item.OwnerKind == "provision-run" {
			item.Orphaned = true
		}
		if item.Ownership == "conflict" {
			item.Message = "Runtime ownership matches more than one resource. Resolve the conflict before deleting data."
		}
		if item.Ownership == "unverified" {
			item.Message = "No verified Dispatch owner. Storage remains protected."
		}
		if err := m.Store.ObserveStorage(ctx, item); err != nil {
			return err
		}
		seen[item.ID] = true
	}
	for _, item := range previous {
		if seen[item.ID] {
			continue
		}
		// A runtime driver cannot prove provider disks absent by listing local volumes.
		if item.Kind == "provider_disk" {
			continue
		}
		item.State, item.Consumers, item.ObservedAt, item.Message = "absent", []core.StorageConsumer{}, time.Now().UTC(), "The target confirmed this storage is absent; ownership history is retained."
		if err := m.Store.ObserveStorage(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

// Scan registered namespaces and retained orphan namespaces individually so
// namespaced target credentials do not need cluster-wide list permission.
func (m *StorageManager) inspectTarget(ctx context.Context, server core.Server, apps []core.App, runs []core.ServiceProvisionRun, previous []core.StorageResource) ([]core.StorageObservation, error) {
	if !core.IsKubernetesRuntime(server.Runtime) || server.Kubernetes == nil {
		return m.Backend.Inspect(ctx, server)
	}
	namespaces := map[string]bool{}
	defaultNamespace := server.Kubernetes.Namespace
	if defaultNamespace == "" {
		defaultNamespace = "default"
	}
	namespaces[defaultNamespace] = true
	for _, app := range apps {
		if app.ServerID == server.ID && app.BuildType == core.BuildTypeHelm {
			namespaces[helmNamespace(app, server)] = true
		}
	}
	for _, run := range runs {
		if run.Target != nil && run.Target.ServerID == server.ID && run.Target.Namespace != "" {
			namespaces[run.Target.Namespace] = true
		}
	}
	for _, item := range previous {
		if item.Kind == "kubernetes_pvc" && item.Namespace != "" {
			namespaces[item.Namespace] = true
		}
	}
	names := []string{}
	for name := range namespaces {
		names = append(names, name)
	}
	sort.Strings(names)
	result := []core.StorageObservation{}
	for _, name := range names {
		scoped := server
		config := *server.Kubernetes
		config.Namespace = name
		scoped.Kubernetes = &config
		items, err := m.Backend.Inspect(ctx, scoped)
		if err != nil {
			return nil, err
		}
		result = append(result, items...)
	}
	return result, nil
}

func StorageID(server, kind, namespace, name string) string {
	return fmt.Sprintf("storage-%x", sha256.Sum256([]byte(server+"\x00"+kind+"\x00"+namespace+"\x00"+name)))
}

// StorageVersion excludes freshness timestamps but includes consumers and policy.
func StorageVersion(items []core.StorageResource) string {
	items = append([]core.StorageResource{}, items...)
	for i := range items {
		items[i].ObservedAt = time.Time{}
	}
	raw, _ := json.Marshal(items)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (m *StorageManager) DeleteLocked(ctx context.Context, item core.StorageResource) error {
	if reason := item.DeleteBlockedReason(); reason != "" {
		return errors.New(reason)
	}
	if item.State == "absent" {
		return nil
	}
	if m.Backend == nil {
		return errors.New("storage deletion is unavailable for this runtime")
	}
	server, err := m.Store.GetServer(ctx, item.ServerID)
	if err != nil {
		return err
	}
	if err := m.Backend.Delete(ctx, server, item); err != nil {
		item.State, item.Message = "inaccessible", "Deletion did not complete; reconcile before retrying."
		_ = m.Store.ObserveStorage(context.Background(), item)
		return errors.New("storage deletion failed; inspect the target and review again before retrying")
	}
	item.State, item.Consumers, item.Message, item.ObservedAt = "absent", []core.StorageConsumer{}, "Confirmed data deletion completed; ownership history is retained.", time.Now().UTC()
	return m.Store.ObserveStorage(ctx, item)
}
