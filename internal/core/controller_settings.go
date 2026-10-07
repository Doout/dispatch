package core

// ControllerSettings controls optional controller workspaces. Disabling a
// workspace does not delete its saved configuration or historical records.
type ControllerSettings struct {
	OperationsEnabled bool            `json:"operationsEnabled"`
	UIFeatures        map[string]bool `json:"uiFeatures"`
}

// ControllerSettingsPatch changes only the supplied settings. UI features
// control navigation and pages, not the underlying APIs or background jobs.
type ControllerSettingsPatch struct {
	OperationsEnabled *bool
	UIFeatures        map[string]bool
}

func UIFeatureNames() []string {
	return []string{
		"machineProvisioning",
		"machineSnapshots",
		"workloadBackups",
		"automationCredentials",
		"infrastructureAssignments",
		"mutationReceipts",
	}
}

func IsUIFeature(name string) bool {
	for _, known := range UIFeatureNames() {
		if name == known {
			return true
		}
	}
	return false
}
