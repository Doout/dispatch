package workflowrunnerexec

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func TestTargetInspectionWorkerRejectsLocalCredentials(t *testing.T) {
	for _, scenario := range []string{"stored credentials", "exec plugin", "host path", "managed worker"} {
		t.Run(scenario, func(t *testing.T) {
			request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "target", RevisionID: "inspect", Mode: "tenant", TargetInspection: &workflowrunner.TargetInspection{
				Config: core.KubernetesServerConfig{Context: "test", Namespace: "app"}, Kubeconfig: serviceKubeconfig,
			}}
			switch scenario {
			case "exec plugin":
				request.TargetInspection.Kubeconfig = strings.Replace(serviceKubeconfig, "token: explicit-worker-credential", "exec:\n      command: /controller/plugin", 1)
			case "host path":
				request.TargetInspection.Config.KubeconfigPath = "/controller/admin.conf"
			case "managed worker":
				request.Mode = "managed"
			}
			called := false
			result := executeTargetInspection(context.Background(), request, func(_ context.Context, config core.KubernetesServerConfig) (core.KubernetesTargetEvidence, error) {
				called = true
				if config.KubeconfigData != serviceKubeconfig || config.KubeconfigPath != "" || config.Namespace != "app" {
					t.Fatal("inspection lost explicit worker credentials or namespace")
				}
				return core.KubernetesTargetEvidence{ClusterUID: "cluster", NamespaceUID: "namespace", Version: "v1.36.0", CheckedAt: time.Now()}, nil
			})
			if scenario == "stored credentials" {
				if !called || result.State != "succeeded" || result.TargetEvidence == nil {
					t.Fatalf("inspection failed: %+v", result)
				}
			} else if called || result.State != "failed" {
				t.Fatal("unsafe target input reached cluster discovery")
			}
		})
	}
}
