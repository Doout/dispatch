package core

// ControllerSettings controls optional controller workspaces. Disabling a
// workspace does not delete its saved configuration or historical records.
type ControllerSettings struct {
	OperationsEnabled bool `json:"operationsEnabled"`
}
