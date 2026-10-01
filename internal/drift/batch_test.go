package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestBatchSharesDiscoveryAndReadsWithoutReusingResultsAfterCompletion(t *testing.T) {
	s, client, app, _ := fixture(t, "")
	ctx := context.Background()
	other := app
	other.ID, other.Name, other.HelmRelease = "second-app", "second", "second"
	if err := s.Store.CreateApp(ctx, other); err != nil {
		t.Fatal(err)
	}
	deployment := core.Deployment{ID: "second-deployment", AppID: other.ID, State: core.DeploymentSucceeded, CreatedAt: time.Now().UTC()}
	if err := s.Store.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("default")
	object, err := resource.Get(ctx, "settings", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	object.SetName("second-settings")
	object.SetUID("second-uid")
	object.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "second", "meta.helm.sh/release-namespace": "default"})
	if _, err := resource.Create(ctx, object, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(object)
	server, _ := s.Store.GetServer(ctx, app.ServerID)
	if err := s.Capture(ctx, deployment, other, server, string(raw)); err != nil {
		t.Fatal(err)
	}
	original := s.Connect
	connections, closes := 0, 0
	s.Connect = func(ctx context.Context, server core.Server) (Connection, error) {
		connections++
		conn, err := original(ctx, server)
		conn.Close = func() { closes++ }
		return conn, err
	}
	client.ClearActions()
	ctx, closeBatch := s.Batch(ctx)
	for _, id := range []string{app.ID, other.ID, app.ID} {
		result, err := s.Check(ctx, id)
		if err != nil || result.State != "synced" {
			t.Fatal(result, err)
		}
	}
	if connections != 1 || closes != 0 || len(client.Actions()) != 2 || client.Actions()[0].GetVerb() != "get" || client.Actions()[1].GetVerb() != "get" {
		t.Fatal("batch repeated discovery or resource reads", connections, closes, client.Actions())
	}
	closeBatch()
	if closes != 1 {
		t.Fatal("batch did not close its connection")
	}
	object, err = resource.Get(context.Background(), "settings", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	object.Object["data"] = map[string]any{"password": "changed"}
	if _, err := resource.Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Check(context.Background(), app.ID)
	if err != nil || result.State != "out_of_sync" || connections != 2 || closes != 2 {
		t.Fatal("manual check reused a prior batch result", result, err, connections, closes)
	}
}

func TestBatchDoesNotShareTargetsOrChangedCredentials(t *testing.T) {
	s := &Service{}
	connections, closes := 0, 0
	s.Connect = func(context.Context, core.Server) (Connection, error) {
		connections++
		return Connection{Close: func() { closes++ }}, nil
	}
	ctx, closeBatch := s.Batch(context.Background())
	server := core.Server{ID: "first", Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "first-credential"}}
	for _, target := range []core.Server{server, server, {ID: "second", Kubernetes: server.Kubernetes}, {ID: "first", Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "replacement-credential"}}} {
		_, closeConnection, err := s.connection(ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		closeConnection()
	}
	closeBatch()
	if connections != 3 || closes != 3 {
		t.Fatal("connections crossed credential or target boundaries", connections, closes)
	}
}

func TestBatchSharesConnectionFailureOnlyUntilNextBatch(t *testing.T) {
	s := &Service{}
	connections := 0
	s.Connect = func(context.Context, core.Server) (Connection, error) {
		connections++
		return Connection{}, errors.New("unavailable")
	}
	server := core.Server{ID: "target"}
	for i := 0; i < 2; i++ {
		ctx, closeBatch := s.Batch(context.Background())
		for j := 0; j < 3; j++ {
			if _, _, err := s.connection(ctx, server); err == nil {
				t.Fatal("connection failure was hidden")
			}
		}
		closeBatch()
	}
	if connections != 2 {
		t.Fatal("failed target was repeatedly contacted or not retried", connections)
	}
}

func TestConnectionReusePreservesEachRequestsContext(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch r.URL.Path {
		case "/api":
			result = metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions", APIVersion: "v1"}, Versions: []string{"v1"}}
		case "/apis":
			result = metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}}
		case "/api/v1":
			result = metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: metav1.Verbs{"get"}}}}
		case "/api/v1/namespaces/default/configmaps/settings":
			result = map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "settings", "namespace": "default"}}
		default:
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer api.Close()
	kubeconfig := fmt.Sprintf("apiVersion: v1\nkind: Config\nclusters:\n- name: test\n  cluster:\n    server: %s\ncontexts:\n- name: test\n  context:\n    cluster: test\n    user: test\ncurrent-context: test\nusers:\n- name: test\n  user: {}\n", api.URL)
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := Connect(ctx, core.Server{Kubernetes: &core.KubernetesServerConfig{KubeconfigData: kubeconfig}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()
	client := conn.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("default")
	if _, err := client.Get(context.Background(), "settings", metav1.GetOptions{}); err != nil {
		t.Fatal("reused client retained the first application's canceled context", err)
	}
	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := client.Get(canceled, "settings", metav1.GetOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("request cancellation was ignored", err)
	}
}

func TestBatchDoesNotReuseExpiredResourceReads(t *testing.T) {
	s, client, app, _ := fixture(t, "")
	ctx, closeBatch := s.Batch(context.Background())
	defer closeBatch()
	if _, err := s.Check(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	b := ctx.Value(batchKey{}).(*checkBatch)
	for key, value := range b.reads {
		value.at = time.Now().Add(-time.Minute)
		b.reads[key] = value
	}
	resource := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace("default")
	object, _ := resource.Get(context.Background(), "settings", metav1.GetOptions{})
	object.Object["data"] = map[string]any{"password": "changed"}
	if _, err := resource.Update(context.Background(), object, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.ClearActions()
	result, err := s.Check(ctx, app.ID)
	if err != nil || result.State != "out_of_sync" || len(client.Actions()) != 1 {
		t.Fatal("expired resource read hid a runtime change", result, err, client.Actions())
	}
}
