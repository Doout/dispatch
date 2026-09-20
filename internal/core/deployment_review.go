package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// DeploymentReview contains application identity, digests, and revision identifiers. The store
// validates it in the transaction that captures the actual runtime bindings.
type DeploymentReview struct {
	ExpectedAppName  string           `json:"expectedAppName"`
	ProjectID        string           `json:"projectId"`
	AppSpecDigest    string           `json:"appSpecDigest"`
	BindingsDigest   string           `json:"bindingsDigest"`
	ServiceRevisions map[string]int64 `json:"serviceRevisions"`
}

func ServiceBindingConfigurationDigest(bindings []ServiceBinding) string {
	ordered := append([]ServiceBinding{}, bindings...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Alias == ordered[j].Alias {
			return ordered[i].ServiceRef < ordered[j].ServiceRef
		}
		return ordered[i].Alias < ordered[j].Alias
	})
	raw, _ := json.Marshal(ordered)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// BoundDeploymentSpecDigest preserves the digest representation used by saved
// deployments, including the original service order from the capture query.
func BoundDeploymentSpecDigest(app string, bindings []ServiceBinding, services []AppliedServiceBinding) string {
	if len(bindings) == 0 {
		return app
	}
	raw, _ := json.Marshal(struct {
		App      string
		Bindings []ServiceBinding
		Services []AppliedServiceBinding
	}{app, bindings, services})
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
