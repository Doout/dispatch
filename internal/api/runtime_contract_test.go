package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/runtimecontract"
)

func TestRuntimeCapabilitiesReflectConfiguredTarget(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	a := handler.(*API)
	a.deploy = deploy.NewService(a.store, deploy.SourceAuthExecutor{Next: deploy.RuntimeExecutor{Default: deploy.DockerExecutor{}, Helm: deploy.HelmExecutor{}}})
	for _, s := range []core.Server{
		{ID: "local", Name: "Local", Address: "local", Runtime: core.ServerRuntimeDocker, CreatedAt: time.Now()},
		{ID: "remote", Name: "Remote", Address: "remote.example.test", Runtime: core.ServerRuntimeDocker, CreatedAt: time.Now()},
	} {
		if err := a.store.CreateServer(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		path      string
		status    int
		supported bool
	}{
		{"/api/v1/servers/local/capabilities", 200, true},
		{"/api/v1/servers/remote/capabilities", 200, false},
		{"/api/v1/servers/local/capabilities?buildType=helm", 200, false},
		{"/api/v1/servers/local/capabilities?buildType=shell", 422, false},
		{"/api/v1/servers/missing/capabilities", 404, false},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
		if tc.status != 200 {
			continue
		}
		var manifest runtimecontract.Manifest
		if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
			t.Fatal(err)
		}
		if (manifest.Check(context.Background(), runtimecontract.Deploy) == nil) != tc.supported {
			t.Fatalf("incorrect capability for %s: %+v", tc.path, manifest)
		}
		if manifest.Check(context.Background(), runtimecontract.Start) == nil {
			t.Fatal("advertised unimplemented start")
		}
	}
}
