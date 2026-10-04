package core

// ApplicationUpdate changes saved deployment inputs without moving the application
// or starting a deployment. Pointers distinguish omitted fields from explicit clears.
type ApplicationUpdate struct {
	ExpectedSpecDigest string     `json:"expectedSpecDigest"`
	SourceRepo         *string    `json:"sourceRepo,omitempty"`
	Branch             *string    `json:"branch,omitempty"`
	BuildType          *BuildType `json:"buildType,omitempty"`
	ContextPath        *string    `json:"contextPath,omitempty"`
	DockerfilePath     *string    `json:"dockerfilePath,omitempty"`
	ComposePath        *string    `json:"composePath,omitempty"`
	ComposeContent     *string    `json:"composeContent,omitempty"`
	Domain             *string    `json:"domain,omitempty"`
	ContainerPort      *int       `json:"containerPort,omitempty"`
	HelmChart          *string    `json:"helmChart,omitempty"`
	HelmVersion        *string    `json:"helmVersion,omitempty"`
	HelmRepository     *string    `json:"helmRepository,omitempty"`
	SourceAuthType     *string    `json:"sourceAuthType,omitempty"`
	SourceCredentialID *string    `json:"sourceCredentialId,omitempty"`
}

func (u ApplicationUpdate) HasChanges() bool {
	return u.SourceRepo != nil || u.Branch != nil || u.BuildType != nil || u.ContextPath != nil || u.DockerfilePath != nil ||
		u.ComposePath != nil || u.ComposeContent != nil || u.Domain != nil || u.ContainerPort != nil || u.HelmChart != nil ||
		u.HelmVersion != nil || u.HelmRepository != nil || u.SourceAuthType != nil || u.SourceCredentialID != nil
}

// BlockingRuntimeJob omits execution payloads, results, leases and caller keys.
// The existing job endpoint provides scoped evidence for the referenced operation.
type BlockingRuntimeJob struct {
	ID           string `json:"id"`
	Operation    string `json:"operation"`
	State        string `json:"state"`
	DeploymentID string `json:"deploymentId,omitempty"`
	Location     string `json:"location"`
}

type ApplicationConfiguration struct {
	App
	SpecDigest         string              `json:"specDigest"`
	BlockingRuntimeJob *BlockingRuntimeJob `json:"blockingRuntimeJob,omitempty"`
}
