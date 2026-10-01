package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func TestServiceResourceDockerRecoveryOwnershipAndDeletion(t *testing.T) {
	req, err := PrepareServiceRequest(provisionRequest())
	if err != nil {
		t.Fatal(err)
	}
	labels := provisionLabels(req)
	exists := true
	removed := 0
	mutated := false
	executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		switch args[0] {
		case "ps":
			if exists {
				io.WriteString(out, "immutable-container-id\n")
			}
		case "inspect":
			json.NewEncoder(out).Encode(map[string]any{"id": "immutable-container-id", "labels": labels, "state": map[string]any{"Status": "running", "Health": map[string]string{"Status": "healthy"}}})
		case "rm":
			if strings.Join(args, " ") != "rm -f immutable-container-id" {
				t.Fatal("cleanup did not address exact container", args)
			}
			removed++
			exists = false
		default:
			mutated = true
		}
		return nil
	}}
	server := core.Server{ID: "server", Runtime: "docker", Address: "local"}
	outputs, err := executor.Provision(context.Background(), req, core.DockerServiceProvision{}, server)
	if err != nil || outputs["connectionUrl"] == "" || mutated {
		t.Fatalf("recovery changed provider: %v", err)
	}
	if !strings.Contains(outputs["connectionUrl"], req.Password) {
		t.Fatal("recovery regenerated credentials")
	}
	labels["dispatch.project"] = "different"
	if _, err = executor.InspectServiceResource(context.Background(), req, server); err == nil {
		t.Fatal("cross-project adoption allowed")
	}
	if err = executor.DeleteServiceResource(context.Background(), req, server, "immutable-container-id"); err == nil || removed > 0 {
		t.Fatal("cross-project deletion allowed")
	}
	labels["dispatch.project"] = req.Run.ProjectID
	if err = executor.DeleteServiceResource(context.Background(), req, server, "replaced-container"); err == nil {
		t.Fatal("stale review deleted replacement")
	}
	for range 2 {
		if err = executor.DeleteServiceResource(context.Background(), req, server, "immutable-container-id"); err != nil {
			t.Fatal(err)
		}
	}
	if removed != 1 || mutated {
		t.Fatal("cleanup repeated mutation or removed retained storage")
	}
}

type serviceReleaseFixture struct {
	recordingHelmClient
	release *helmrelease.Release
	missing bool
}

func (c *serviceReleaseFixture) InspectServiceRelease(context.Context, string) (*helmrelease.Release, error) {
	if c.missing {
		return nil, driver.ErrReleaseNotFound
	}
	return c.release, nil
}
func TestServiceResourceHelmOwnershipAndConvergentDeletion(t *testing.T) {
	req := provisionRequest()
	req.Password = "retained-fixture-password"
	metadata := newHelmDeploymentMetadata(core.App{ID: req.Run.ID, ProjectID: req.Run.ProjectID, HelmProvenance: core.HelmProvenance{WorkflowResourceID: req.Run.TemplateID}}, core.Deployment{ID: req.Run.ID})
	client := &serviceReleaseFixture{release: &helmrelease.Release{Name: ServiceResourceName(req.Run.ID), Namespace: "data", Version: 1, Info: &helmrelease.Info{Status: helmrelease.StatusDeployed, Description: metadata.description()}}}
	executor := HelmExecutor{newClient: func(core.Server, string, string) (helmClient, error) { return client, nil }}
	server := core.Server{ID: "cluster", Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/unused", Namespace: "data"}}
	spec := core.HelmServiceProvision{Namespace: "data"}
	inspected, err := executor.InspectServiceResource(context.Background(), req, spec, server)
	if err != nil || inspected.State != "ready" {
		t.Fatal(inspected, err)
	}
	outputs, err := executor.Provision(context.Background(), req, spec, server)
	if err != nil || !strings.Contains(outputs["connectionUrl"], req.Password) || client.operation != "" {
		t.Fatal("Helm recovery mutated an existing release or regenerated credentials", err)
	}
	wrong := req
	wrong.Run.ProjectID = "other"
	if _, err = executor.InspectServiceResource(context.Background(), wrong, spec, server); err == nil {
		t.Fatal("Helm adopted another project's release")
	}
	if _, err = executor.Provision(context.Background(), wrong, spec, server); err == nil || client.operation != "" {
		t.Fatal("Helm provisioning overwrote another project's release")
	}
	if err = executor.DeleteServiceResource(context.Background(), req, spec, server, "wrong-version"); err == nil || client.operation != "" {
		t.Fatal("Helm cleanup ignored reviewed identity")
	}
	if err = executor.DeleteServiceResource(context.Background(), req, spec, server, inspected.ResourceID); err != nil || client.operation != "uninstall" {
		t.Fatal("owned release not removed", err)
	}
	client.missing = true
	client.operation = ""
	if err = executor.DeleteServiceResource(context.Background(), req, spec, server, inspected.ResourceID); err != nil || client.operation != "" {
		t.Fatal("already absent release did not converge", err)
	}
}

func TestServiceResourceRetainedVolumeRejectsForeignOwnership(t *testing.T) {
	req, _ := PrepareServiceRequest(provisionRequest())
	executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		if args[0] == "ps" {
			return nil
		}
		if args[0] == "volume" && args[1] == "ls" {
			io.WriteString(out, ServiceResourceName(req.Run.ID)+"-data")
			return nil
		}
		if args[0] == "volume" && args[1] == "inspect" {
			io.WriteString(out, `{"dispatch.project":"someone-else"}`)
			return nil
		}
		return errors.New("unexpected mutation")
	}}
	if _, err := executor.Provision(context.Background(), req, core.DockerServiceProvision{}, core.Server{Runtime: "docker", Address: "local"}); err == nil || !strings.Contains(err.Error(), "different provision run") {
		t.Fatal("foreign retained data was reused", err)
	}
}
