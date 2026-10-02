package core

type ReleaseResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type RuntimeRollbackPreview struct {
	Available           bool                    `json:"available"`
	Message             string                  `json:"message"`
	DeploymentID        string                  `json:"deploymentId"`
	CurrentDeploymentID string                  `json:"currentDeploymentId"`
	HelmRevision        int                     `json:"helmRevision,omitempty"`
	Bindings            []AppliedServiceBinding `json:"bindings"`
	Resources           []ReleaseResource       `json:"resources"`
	ReviewDigest        string                  `json:"reviewDigest"`
	Runtime             string                  `json:"runtime,omitempty"`
	Target              string                  `json:"target,omitempty"`
	Images              map[string]string       `json:"images,omitempty"`
	Domain              string                  `json:"domain,omitempty"`
	Ports               []string                `json:"ports,omitempty"`
	ContainerPort       int                     `json:"containerPort,omitempty"`
	RuntimeDigest       string                  `json:"runtimeDigest,omitempty"`
}
