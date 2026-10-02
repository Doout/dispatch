package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestKubernetesTargetUpdateCannotChangeRecordedIdentity(t *testing.T) {
	prior := &core.KubernetesServerConfig{Validation: &core.KubernetesTargetEvidence{ClusterUID: "cluster-a", NamespaceUID: "namespace-a"}}
	for _, tc := range []struct {
		name, cluster, namespace string
		failure                  bool
		status                   int
	}{
		{"credential rotation", "cluster-a", "namespace-a", false, 200},
		{"cluster replacement", "cluster-b", "namespace-a", false, 409},
		{"namespace recreation", "cluster-a", "namespace-b", false, 409},
		{"lost permissions", "", "", true, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := API{kubernetesTargetValidator: func(context.Context, core.KubernetesServerConfig) (core.KubernetesTargetEvidence, error) {
				if tc.failure {
					return core.KubernetesTargetEvidence{}, errors.New("Selected cluster is unavailable.")
				}
				return core.KubernetesTargetEvidence{ClusterUID: tc.cluster, NamespaceUID: tc.namespace}, nil
			}}
			candidate := &core.KubernetesServerConfig{}
			w := httptest.NewRecorder()
			accepted := a.inspectKubernetesTarget(w, httptest.NewRequest(http.MethodPut, "/", nil), candidate, prior)
			if w.Code != tc.status || accepted != (tc.status == 200) {
				t.Fatalf("accepted=%v status=%d body=%s", accepted, w.Code, w.Body)
			}
			if !accepted && candidate.Validation != nil {
				t.Fatal("failed validation saved evidence")
			}
			if strings.Contains(w.Body.String(), "cluster-a") || strings.Contains(w.Body.String(), "namespace-b") {
				t.Fatal("response exposes internal target identifiers")
			}
		})
	}
}
