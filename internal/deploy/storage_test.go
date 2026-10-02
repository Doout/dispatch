package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

type storageFake struct {
	items   []core.StorageObservation
	err     error
	deletes int
}

func (f *storageFake) Inspect(context.Context, core.Server) ([]core.StorageObservation, error) {
	return f.items, f.err
}
func (f *storageFake) Delete(context.Context, core.Server, core.StorageResource) error {
	f.deletes++
	return f.err
}

func TestStorageReconciliationPreservesOwnershipAndUnknownOutcomes(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "p", Name: "p", CreatedAt: now}
	server := core.Server{ID: "s", Name: "s", Runtime: core.ServerRuntimeDocker, Address: "local", State: "ready", CreatedAt: now}
	app := core.App{ID: "a", Name: "a", ProjectID: "p", ServerID: "s", BuildType: core.BuildTypeDockerfile, Generated: true, State: "closed", CreatedAt: now}
	if err = data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err = data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err = data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"dispatch.app": "a"}
	backend := &storageFake{items: []core.StorageObservation{{Resource: core.StorageResource{Kind: "docker_volume", Name: "data", Identity: "first", Evidence: storageEvidence(labels)}, Labels: labels}, {Resource: core.StorageResource{Kind: "docker_volume", Name: "external", Identity: "other"}, Labels: map[string]string{}}}}
	m := NewStorageManager(data)
	m.Backend = backend
	if err = m.Refresh(ctx, server); err != nil {
		t.Fatal(err)
	}
	id := StorageID("s", "docker_volume", "", "data")
	item, _ := data.GetStorage(ctx, id)
	if item.Ownership != "verified" || item.Policy != "retain" || item.OwnerID != "a" || !item.Orphaned {
		t.Fatalf("adoption: %+v", item)
	}
	claim := core.StorageResource{ID: "retained-claim", ServerID: server.ID, Kind: "provider_disk", Name: "claim", Namespace: "preview", Identity: "disk", Ownership: "verified", State: "present", Consumers: []core.StorageConsumer{}}
	if err = data.ObserveStorage(ctx, claim); err != nil {
		t.Fatal(err)
	}
	called := false
	if err = m.CleanupNamespace(ctx, server, "preview", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("namespace cleanup bypassed retained storage")
	}
	claim.OwnerID, claim.OwnerKind, claim.ProjectID = app.ID, "application", project.ID
	claim.Consumers = []core.StorageConsumer{{ID: "owner:StatefulSet:preview/database", Active: true}}
	if err = data.ObserveStorage(ctx, claim); err != nil {
		t.Fatal(err)
	}
	helmApp := app
	helmApp.BuildType = core.BuildTypeHelm
	helmApp.HelmNamespace = "preview"
	if err = m.CheckApplicationCleanup(ctx, helmApp, server); err == nil {
		t.Fatal("live garbage-collection owner bypassed retained-data cleanup guard")
	}

	external, _ := data.GetStorage(ctx, StorageID("s", "docker_volume", "", "external"))
	if external.Ownership != "unverified" || external.ProjectID != "" || external.DeleteBlockedReason() == "" {
		t.Fatal("unowned volume adopted")
	}
	if err = data.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Refresh(ctx, server); err != nil {
		t.Fatal(err)
	}
	item, _ = data.GetStorage(ctx, id)
	if !item.Orphaned || item.OwnerID != "a" || item.Ownership != "verified" {
		t.Fatalf("lost orphan provenance: %+v", item)
	}
	backend.err = errors.New("connection refused")
	if err = m.Refresh(ctx, server); err == nil {
		t.Fatal("inspection failure accepted")
	}
	item, _ = data.GetStorage(ctx, id)
	if item.State != "inaccessible" {
		t.Fatal("inspection failure became absence")
	}
	backend.err = nil
	backend.items = nil
	if err = m.Refresh(ctx, server); err != nil {
		t.Fatal(err)
	}
	item, _ = data.GetStorage(ctx, id)
	if item.State != "absent" || item.OwnerID != "a" {
		t.Fatal("confirmed absence discarded history")
	}
}

func TestStorageDockerDeletionRejectsReplacedOrConsumedVolumes(t *testing.T) {
	for _, test := range []struct {
		name, created, containers string
		wantDelete                bool
	}{{"unused", "original", "", true}, {"replaced", "replacement", "", false}, {"stopped-consumer", "original", "container1", false}} {
		t.Run(test.name, func(t *testing.T) {
			deleted := false
			r := RuntimeStorage{Run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
				switch strings.Join(args[:2], " ") {
				case "volume ls":
					io.WriteString(out, "data\n")
				case "volume inspect":
					io.WriteString(out, `{"Name":"data","CreatedAt":"`+test.created+`","Driver":"local","Labels":{"dispatch.app":"a"}}`)
				case "ps -aq":
					io.WriteString(out, test.containers)
				case "inspect --format":
					io.WriteString(out, `{"id":"container1","active":false,"mounts":[{"Type":"volume","Name":"data","Destination":"/data"}]}`)
				case "volume rm":
					if strings.Join(args, " ") != "volume rm data" {
						t.Fatal("forced data deletion")
					}
					deleted = true
				default:
					t.Fatalf("unexpected command %v", args)
				}
				return nil
			}}
			item := core.StorageResource{Kind: "docker_volume", Name: "data", Identity: "original:local", Evidence: storageEvidence(map[string]string{"dispatch.app": "a"})}
			err := r.Delete(context.Background(), core.Server{Runtime: core.ServerRuntimeDocker, Address: "local"}, item)
			if deleted != test.wantDelete || test.wantDelete != (err == nil) {
				t.Fatalf("deleted=%v err=%v", deleted, err)
			}
		})
	}
}

func TestStorageKubernetesPVCConsumersAndUID(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "test", UID: "original", Labels: map[string]string{"dispatch.app/app-id": "a", "dispatch.app/managed-by": "dispatch"}}}, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "test"}, Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data"}}}}}})
	r := RuntimeStorage{Kubernetes: func(core.Server) (kubernetes.Interface, error) { return client, nil }}
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Namespace: "test"}}
	items, err := r.Inspect(ctx, server)
	if err != nil || len(items) != 1 || len(items[0].Resource.Consumers) != 1 {
		t.Fatalf("inventory: %+v %v", items, err)
	}
	item := items[0].Resource
	if err = r.Delete(ctx, server, item); err == nil {
		t.Fatal("deleted consumed PVC")
	}
	if err = client.CoreV1().Pods("test").Delete(ctx, "consumer", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	item.Identity = "replacement"
	if err = r.Delete(ctx, server, item); err == nil {
		t.Fatal("accepted mismatched PVC UID")
	}
	item.Identity = "original"
	if err = r.Delete(ctx, server, item); err != nil {
		t.Fatal(err)
	}
	if err = r.Delete(ctx, server, item); err != nil {
		t.Fatal("retry of absent PVC failed", err)
	}
}

func TestStorageHelmRetentionAndLegacyCleanup(t *testing.T) {
	manifest := "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data\nspec: {}\n"
	if err := checkHelmStorageCleanup(manifest); err == nil {
		t.Fatal("legacy unprotected claim can be uninstalled")
	}
	metadata := newHelmDeploymentMetadata(core.App{ID: "app"}, core.Deployment{ID: "deployment"})
	rendered, err := metadata.Run(bytes.NewBufferString(manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err = checkHelmStorageCleanup(rendered.String()); err != nil {
		t.Fatal("retained claim blocked", err)
	}
	for _, unsafe := range []string{"kind: Namespace\nmetadata: {name: test}\n", "kind: StatefulSet\nspec:\n  persistentVolumeClaimRetentionPolicy: {whenDeleted: Delete}\n"} {
		if err := checkHelmStorageCleanup(unsafe); err == nil {
			t.Fatal("accepted unsafe cascading cleanup")
		}
	}
}

func TestStorageNamespaceCleanupInspectsUnregisteredPreview(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "namespace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	server := core.Server{ID: "target", Name: "target", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Namespace: "default"}, CreatedAt: time.Now().UTC()}
	if err = data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientset(&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "retained", Namespace: "forgotten-preview", UID: "claim-uid"}})
	manager := NewStorageManager(data)
	manager.Backend = RuntimeStorage{Kubernetes: func(core.Server) (kubernetes.Interface, error) { return client, nil }}
	called := false
	if err = manager.CleanupNamespace(ctx, server, "forgotten-preview", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("unregistered preview namespace was deleted with PVC data")
	}
	item, err := data.GetStorage(ctx, StorageID(server.ID, "kubernetes_pvc", "forgotten-preview", "retained"))
	if err != nil || item.State != "present" || item.Ownership != "unverified" {
		t.Fatalf("forgotten storage was not protected: %+v %v", item, err)
	}
}
