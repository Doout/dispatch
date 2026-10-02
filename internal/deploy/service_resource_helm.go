package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/action"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// Prepared service runs create once; recovery adopts a verified release instead of upgrading it.
type serviceInstallOnlyKey struct{}

type serviceReleaseClient interface {
	InspectServiceRelease(context.Context, string) (*helmrelease.Release, error)
}

func (c *sdkHelmClient) InspectServiceRelease(ctx context.Context, name string) (*helmrelease.Release, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return action.NewGet(c.configuration).Run(name)
}
func (e HelmExecutor) serviceResourceClient(ctx context.Context, server core.Server, namespace string) (helmClient, func(), error) {
	prepared, cleanup, err := prepareKubernetesServer(ctx, server)
	if err != nil {
		return nil, func() {}, errors.New("cannot load service target credentials")
	}
	root, err := os.MkdirTemp("", "dispatch-service-inspect-")
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	done := func() { os.RemoveAll(root); cleanup() }
	client, err := e.client(prepared, namespace, root)
	if err != nil {
		done()
		return nil, func() {}, errors.New("cannot inspect the Helm service target")
	}
	return client, done, nil
}
func inspectServiceRelease(ctx context.Context, client helmClient, req core.ServiceProvisionRequest, server core.Server, namespace string) (core.ServiceResourceInspection, error) {
	result := serviceInspection(req, server, "helm")
	inspector, ok := client.(serviceReleaseClient)
	if !ok {
		return result, errors.New("Helm client does not support owned service inspection")
	}
	release, err := inspector.InspectServiceRelease(ctx, ServiceResourceName(req.Run.ID))
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return result, nil
	}
	if err != nil || release == nil || release.Info == nil {
		return result, errors.New("cannot inspect the owned Helm service release")
	}
	var metadata helmDeploymentMetadata
	if json.Unmarshal([]byte(strings.TrimPrefix(release.Info.Description, "Dispatch provenance: ")), &metadata) != nil || metadata.AppID != req.Run.ID || metadata.DeploymentID != req.Run.ID || metadata.ProjectID != req.Run.ProjectID || metadata.Provenance.WorkflowResourceID != req.Run.TemplateID || release.Name != ServiceResourceName(req.Run.ID) || release.Namespace != namespace {
		return result, errors.New("Helm service ownership does not match its project, template and provision run")
	}
	result.ResourceID = fmt.Sprintf("%s/%s:%d:%s", namespace, release.Name, release.Version, release.Info.FirstDeployed.String())
	result.StorageRetained = checkHelmStorageCleanup(release.Manifest) == nil
	result.State = "unready"
	if release.Info.Status == helmrelease.StatusDeployed {
		result.State = "ready"
	}
	return result, nil
}
func (e HelmExecutor) InspectServiceResource(ctx context.Context, req core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server) (core.ServiceResourceInspection, error) {
	result := serviceInspection(req, server, "helm")
	if err := ValidateServiceTarget(server, "helm"); err != nil {
		return result, err
	}
	namespace := ServiceProvisionNamespace(spec, server)
	client, cleanup, err := e.serviceResourceClient(ctx, server, namespace)
	if err != nil {
		return result, err
	}
	defer cleanup()
	return inspectServiceRelease(ctx, client, req, server, namespace)
}
func (e HelmExecutor) DeleteServiceResource(ctx context.Context, req core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server, expected string) error {
	if err := ValidateServiceTarget(server, "helm"); err != nil {
		return err
	}
	namespace := ServiceProvisionNamespace(spec, server)
	client, cleanup, err := e.serviceResourceClient(ctx, server, namespace)
	if err != nil {
		return err
	}
	defer cleanup()
	current, err := inspectServiceRelease(ctx, client, req, server, namespace)
	if err != nil {
		return err
	}
	if current.State == "absent" {
		return nil
	}
	if current.ResourceID != expected || expected == "" {
		return errors.New("Helm service changed after deletion review")
	}
	owner := core.App{ID: req.Run.ID, ProjectID: req.Run.ProjectID, HelmRelease: ServiceResourceName(req.Run.ID), HelmNamespace: namespace}
	if err = client.Uninstall(ctx, ServiceResourceName(req.Run.ID), owner); err != nil {
		return errors.New("Helm service cleanup refused or did not finish; protected storage must remain retained")
	}
	return nil
}
