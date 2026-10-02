package core

import "time"

// RepositoryStatus records only public repository identity and recovery evidence.
// A missing private repository is inaccessible until a signed deletion establishes
// absence. A name is never sufficient evidence that two repositories are equal.
type RepositoryStatus struct {
	State        string    `json:"state"`
	RepositoryID int64     `json:"repositoryId,omitempty"`
	FullName     string    `json:"fullName,omitempty"`
	Detail       string    `json:"detail"`
	Recovery     string    `json:"recovery"`
	CheckedAt    time.Time `json:"checkedAt"`
}
