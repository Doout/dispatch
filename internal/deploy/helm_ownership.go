package deploy

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
	"gopkg.in/yaml.v3"
	helmrelease "helm.sh/helm/v3/pkg/release"
)

// Reusing a release name never grants ownership. Older Dispatch releases may
// lack a project ID; their globally unique application ID still has to match.
func requireHelmReleaseOwner(release *helmrelease.Release, app core.App, namespace string) error {
	conflict := &runtimecontract.Error{Code: runtimecontract.OwnershipConflict, Message: "The Helm release does not belong to this Dispatch application and namespace. Choose another release name or inspect its ownership before retrying."}
	if release == nil || release.Info == nil || app.ID == "" || release.Name != helmReleaseName(app) || release.Namespace != namespace {
		return conflict
	}
	valid := func(raw string) bool {
		var metadata helmDeploymentMetadata
		return json.Unmarshal([]byte(raw), &metadata) == nil && metadata.AppID == app.ID && metadata.DeploymentID != "" && (metadata.ProjectID == "" || metadata.ProjectID == app.ProjectID)
	}
	if strings.HasPrefix(release.Info.Description, "Dispatch provenance: ") {
		if !valid(strings.TrimPrefix(release.Info.Description, "Dispatch provenance: ")) {
			return conflict
		}
		return nil
	}
	// Helm replaces descriptions after rollback and failed upgrades. Retained
	// manifests still carry Dispatch's original ownership on every document.
	decoder := yaml.NewDecoder(strings.NewReader(release.Manifest))
	owned := false
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return conflict
		}
		if len(document) == 0 {
			continue
		}
		metadata, _ := document["metadata"].(map[string]any)
		annotations, _ := metadata["annotations"].(map[string]any)
		provenance, _ := annotations[helmProvenanceAnnotation].(string)
		if !valid(provenance) {
			return conflict
		}
		owned = true
	}
	if owned {
		return nil
	}
	// Empty charts have no resource annotations. New releases retain labels
	// inside Helm's release record so an empty-chart rollback remains owned.
	if release.Labels["dispatch.app/managed-by"] == "dispatch" && release.Labels["dispatch.app/app-id"] == app.ID && release.Labels["dispatch.app/project-id"] == app.ProjectID && release.Labels["dispatch.app/deployment-id"] != "" {
		return nil
	}
	return conflict
}
