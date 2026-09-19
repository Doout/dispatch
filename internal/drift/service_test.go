package drift

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, dsn string) (*Service, *dynamicfake.FakeDynamicClient, core.App, core.Deployment) {
	t.Helper()
	ctx := context.Background()
	if dsn == "" {
		dsn = filepath.Join(t.TempDir(), "drift.db")
	}
	data, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	app := core.App{ID: "drift-app", Name: "drift", ProjectID: "drift-project", ServerID: "drift-server", BuildType: core.BuildTypeHelm, HelmNamespace: "default", HelmRelease: "drift", CreatedAt: now}
	for _, err := range []error{data.CreateProject(ctx, core.Project{ID: app.ProjectID, Name: app.ProjectID, CreatedAt: now}), data.CreateServer(ctx, core.Server{ID: app.ServerID, Name: "drift", Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{Namespace: "default"}, CreatedAt: now}), data.CreateApp(ctx, app)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	d := core.Deployment{ID: "drift-deployment", AppID: app.ID, CommitSHA: "test", SpecDigest: app.SpecDigest(), State: core.DeploymentSucceeded, CreatedAt: now}
	if err = data.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "settings", "namespace": "default", "uid": "config-uid", "resourceVersion": "1", "annotations": map[string]any{"meta.helm.sh/release-name": "drift", "meta.helm.sh/release-namespace": "default"}, "labels": map[string]any{"app.kubernetes.io/managed-by": "Helm"}}, "data": map[string]any{"password": "saved-secret"}}}
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	s := New(data, vault)
	s.Connect = func(context.Context, core.Server) (Connection, error) {
		return Connection{Dynamic: client, Mapper: mapper, Close: func() {}}, nil
	}
	raw, _ := json.Marshal(obj)
	server, _ := data.GetServer(ctx, app.ServerID)
	if err = s.Capture(ctx, d, app, server, string(raw)); err != nil {
		t.Fatal(err)
	}
	return s, client, app, d
}
func TestEncryptedBaselineUnknownAndLastSuccessfulCheck(t *testing.T) {
	s, client, app, d := fixture(t, "")
	ctx := context.Background()
	b, err := s.Store.GetDriftBaseline(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.Ciphertext, "saved-secret") {
		t.Fatal("baseline stored plaintext")
	}
	result, err := s.Check(ctx, app.ID)
	if err != nil || result.State != "synced" || result.LastSuccessfulCheckAt == nil {
		t.Fatal(result, err)
	}
	originalTime := *result.LastSuccessfulCheckAt
	resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("default")
	obj, _ := resource.Get(ctx, "settings", metav1.GetOptions{})
	obj.Object["data"] = map[string]any{"password": "changed-secret"}
	if _, err = resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = s.Check(ctx, app.ID)
	if err != nil || result.State != "out_of_sync" {
		t.Fatal(result, err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "saved-secret") || strings.Contains(string(raw), "changed-secret") {
		t.Fatal("check leaked credential")
	}
	originalTime = *result.LastSuccessfulCheckAt
	s.Connect = func(context.Context, core.Server) (Connection, error) {
		return Connection{}, errors.New("credential-in-error")
	}
	result, err = s.Check(ctx, app.ID)
	if err != nil || result.State != "unknown" || result.Health != "unknown" || !result.LastSuccessfulCheckAt.Equal(originalTime) {
		t.Fatal(result, err)
	}
	if strings.Contains(result.Message, "credential-in-error") {
		t.Fatal("connection error leaked")
	}
	saved, err := s.Store.GetDriftCheck(ctx, app.ID)
	if err != nil || saved.State != "unknown" {
		t.Fatal("unknown observation was not persisted", err)
	}
}
func TestReapplyRejectsStaleDeploymentAndPreflightsBeforeWrites(t *testing.T) {
	s, client, app, d := fixture(t, "")
	ctx := context.Background()
	if _, err := s.Reapply(ctx, app.ID, "old-deployment", "operator"); err == nil {
		t.Fatal("accepted stale deployment")
	}
	resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("default")
	obj, _ := resource.Get(ctx, "settings", metav1.GetOptions{})
	obj.Object["data"] = map[string]any{"password": "changed"}
	resource.Update(ctx, obj, metav1.UpdateOptions{})
	client.PrependReactor("patch", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		patch := action.(ktesting.PatchActionImpl)
		if len(patch.GetPatchOptions().DryRun) == 0 {
			t.Fatal("real write attempted before all preflights succeeded")
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "settings", errors.New("secret-server-error"))
	})
	if _, err := s.Reapply(ctx, app.ID, d.ID, "operator"); err == nil || strings.Contains(err.Error(), "secret-server-error") {
		t.Fatal("missing sanitized preflight error", err)
	}
	actions, err := s.Store.ListDriftActions(ctx, app.ID)
	if err != nil || len(actions) != 1 || actions[0].State != "failed" || actions[0].Actor != "operator" {
		t.Fatal(actions, err)
	}
}
func TestDriftPostgresPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_DRIFT_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_DRIFT_POSTGRES_URL to a disposable empty PostgreSQL database")
	}
	s, _, app, d := fixture(t, dsn)
	check, err := s.Check(context.Background(), app.ID)
	if err != nil || check.State != "synced" {
		t.Fatal(check, err)
	}
	b, err := s.Store.GetDriftBaseline(context.Background(), d.ID)
	if err != nil || b.Ciphertext == "" {
		t.Fatal(b, err)
	}
}

func TestTimedOutObservationReplacesSavedGreenResult(t *testing.T) {
	s, _, app, _ := fixture(t, "")
	if _, err := s.Check(context.Background(), app.ID); err != nil {
		t.Fatal(err)
	}
	s.Connect = func(ctx context.Context, _ core.Server) (Connection, error) {
		<-ctx.Done()
		return Connection{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	check, err := s.Check(ctx, app.ID)
	if err != nil || check.State != "unknown" || check.LastSuccessfulCheckAt == nil {
		t.Fatal(check, err)
	}
	saved, err := s.Store.GetDriftCheck(context.Background(), app.ID)
	if err != nil || saved.State != "unknown" {
		t.Fatal(saved, err)
	}
}
