package core

import "time"

type InfrastructureProviderRule struct {
	ProviderID string   `json:"providerId"`
	Regions    []string `json:"regions"`
	Sizes      []string `json:"sizes"`
	AnyRegion  bool     `json:"anyRegion"`
	AnySize    bool     `json:"anySize"`
}
type InfrastructureQuotaPolicy struct {
	ProjectID                   string                       `json:"projectId"`
	Configured                  bool                         `json:"configured"`
	Revision                    int64                        `json:"revision"`
	MaxServers                  int64                        `json:"maxServers"`
	MaxTemporaryEnvironments    int64                        `json:"maxTemporaryEnvironments"`
	MaxServices                 int64                        `json:"maxServices"`
	MaxSnapshots                int64                        `json:"maxSnapshots"`
	MaxTemporaryLifetimeSeconds int64                        `json:"maxTemporaryLifetimeSeconds"`
	Providers                   []InfrastructureProviderRule `json:"providers"`
	UpdatedAt                   time.Time                    `json:"updatedAt"`
}
type InfrastructureQuotaReservation struct {
	ServerID    string    `json:"serverId"`
	OperationID string    `json:"operationId"`
	ProjectID   string    `json:"projectId"`
	ProviderID  string    `json:"providerId"`
	Region      string    `json:"region"`
	Size        string    `json:"size"`
	State       string    `json:"state"`
	ResourceID  string    `json:"resourceId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// QuotaChange contains only immutable ownership and public allocation choices.
// It never accepts provider credentials or executable bootstrap data.
type InfrastructureQuotaChange struct {
	ProjectID, ProviderID, OperationID, ServerID, ResourceID, Action, Region, Size string
}
type InfrastructureQuotaViolation struct {
	Code       string `json:"code"`
	Limit      string `json:"limit"`
	Maximum    int64  `json:"maximum"`
	Usage      int64  `json:"usage"`
	Requested  int64  `json:"requested"`
	ProviderID string `json:"providerId,omitempty"`
	Region     string `json:"region,omitempty"`
	Size       string `json:"size,omitempty"`
}

func (e *InfrastructureQuotaViolation) Error() string {
	return "infrastructure admission denied by project resource policy"
}
