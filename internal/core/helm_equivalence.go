package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// HelmEquivalence is private proof that evaluated inputs produce the retained
// release. It never changes that release's immutable deployment record.
type HelmEquivalence struct {
	AppID               string    `json:"-"`
	ProjectID           string    `json:"-"`
	AppName             string    `json:"-"`
	AppSpecDigest       string    `json:"-"`
	CandidateSpecDigest string    `json:"-"`
	ChartCommit         string    `json:"-"`
	ServerID            string    `json:"-"`
	TargetDigest        string    `json:"-"`
	DeploymentID        string    `json:"-"`
	CheckedAt           time.Time `json:"-"`
}

func privateInputDigest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// HelmTargetDigest includes stored private target inputs. Never expose this
// credential-derived value in an API response, log, or public snapshot.
func HelmTargetDigest(server Server) string {
	data, ca := "", ""
	if server.Kubernetes != nil {
		data, ca = server.Kubernetes.KubeconfigData, server.Kubernetes.CertificateAuthorityData
	}
	return privateInputDigest(struct {
		ID, Address, Runtime, State, AgentMode string
		Kubernetes                             *KubernetesServerConfig
		KubeconfigData, CertificateAuthority   string
	}{server.ID, server.Address, server.Runtime, server.State, server.AgentMode, server.Kubernetes, data, ca})
}

func HelmEquivalenceFingerprint(proof HelmEquivalence) string {
	return privateInputDigest([]string{proof.AppID, proof.ProjectID, proof.AppName, proof.AppSpecDigest, proof.CandidateSpecDigest, proof.ChartCommit, proof.ServerID, proof.TargetDigest, proof.DeploymentID, proof.CheckedAt.UTC().Format(time.RFC3339Nano)})
}

type WorkflowDeploymentResult struct {
	DeploymentName string    `json:"deploymentName"`
	AppID          string    `json:"appId"`
	DeploymentID   string    `json:"deploymentId"`
	Outcome        string    `json:"outcome"`
	Reason         string    `json:"reason,omitempty"`
	CheckedAt      time.Time `json:"checkedAt"`
}

// WorkflowEquivalence records an evaluated source snapshot without creating a
// workflow execution or a deployment. Private anchors must still match on read.
type WorkflowEquivalence struct {
	ResourceID         string                            `json:"resourceId"`
	ProjectID          string                            `json:"-"`
	SpecDigest         string                            `json:"-"`
	BaselineRevisionID string                            `json:"baselineRevisionId"`
	Sources            map[string]WorkflowSourceRevision `json:"sources"`
	Results            []WorkflowDeploymentResult        `json:"results"`
	AppProofs          map[string]string                 `json:"-"`
	CheckedAt          time.Time                         `json:"checkedAt"`
}
