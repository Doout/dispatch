package core

import "time"

// StorageResource survives deletion of its workload and target registration.
// Identity identifies a particular runtime object, not merely a reusable name.
type StorageResource struct {
	ID             string            `json:"id"`
	ServerID       string            `json:"serverId"`
	Kind           string            `json:"kind"`
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace,omitempty"`
	Identity       string            `json:"identity"`
	Evidence       string            `json:"evidence"`
	ProjectID      string            `json:"projectId,omitempty"`
	OwnerKind      string            `json:"ownerKind,omitempty"`
	OwnerID        string            `json:"ownerId,omitempty"`
	ProvisionRunID string            `json:"provisionRunId,omitempty"`
	Ownership      string            `json:"ownership"`
	Orphaned       bool              `json:"orphaned"`
	Policy         string            `json:"policy"`
	State          string            `json:"state"`
	Consumers      []StorageConsumer `json:"consumers"`
	Mounts         []string          `json:"mounts"`
	// Independent records whether the provider can preserve storage without its server.
	Independent bool      `json:"independent"`
	Revision    int64     `json:"revision"`
	ObservedAt  time.Time `json:"observedAt"`
	Message     string    `json:"message,omitempty"`
}

type StorageConsumer struct {
	ID     string `json:"id"`
	Mount  string `json:"mount,omitempty"`
	Active bool   `json:"active"`
}

// StorageObservation is runtime evidence. Labels are never accepted from API callers.
type StorageObservation struct {
	Resource StorageResource
	Labels   map[string]string
}

func (s StorageResource) DeleteBlockedReason() string {
	if s.Ownership != "verified" {
		return "Storage ownership is not verified. Inspect its runtime labels and owner before deleting data."
	}
	if s.State != "present" && s.State != "absent" {
		return "Storage is not currently inspectable. Reconcile the target before deleting data."
	}
	if s.Policy != "destroy" {
		return "Storage is retained. Change its policy explicitly before reviewing data deletion."
	}
	if len(s.Consumers) != 0 {
		return "Storage still has runtime consumers. Remove those workloads before deleting data."
	}
	return ""
}
