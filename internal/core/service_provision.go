package core

// Built-in provisioners keep the connection contract independent of deployment.
// Script provisioners remain available for providers with a separate API.
type DockerServiceProvision struct {
	ServerRef        string            `json:"serverRef" yaml:"serverRef"`
	Image            string            `json:"image,omitempty" yaml:"image,omitempty"`
	Network          string            `json:"network,omitempty" yaml:"network,omitempty"`
	StorageMountPath string            `json:"storageMountPath,omitempty" yaml:"storageMountPath,omitempty"`
	Environment      map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Healthcheck      []string          `json:"healthcheck,omitempty" yaml:"healthcheck,omitempty"`
	Connection       map[string]string `json:"connection,omitempty" yaml:"connection,omitempty"`
}

type HelmServiceProvision struct {
	ServerRef    string            `json:"serverRef" yaml:"serverRef"`
	Namespace    string            `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Image        string            `json:"image,omitempty" yaml:"image,omitempty"`
	Storage      string            `json:"storage,omitempty" yaml:"storage,omitempty"`
	StorageClass string            `json:"storageClass,omitempty" yaml:"storageClass,omitempty"`
	Chart        string            `json:"chart,omitempty" yaml:"chart,omitempty"`
	Repository   string            `json:"repository,omitempty" yaml:"repository,omitempty"`
	Version      string            `json:"version,omitempty" yaml:"version,omitempty"`
	Values       map[string]any    `json:"values,omitempty" yaml:"values,omitempty"`
	Connection   map[string]string `json:"connection,omitempty" yaml:"connection,omitempty"`
}

type ServiceProvisionTarget struct {
	Provider     string `json:"provider"`
	ServerID     string `json:"serverId"`
	ResourceName string `json:"resourceName"`
	Namespace    string `json:"namespace,omitempty"`
}

type ServiceProvisionRequest struct {
	Run         ServiceProvisionRun
	ServiceType string
	Outputs     []string
	Inputs      map[string]string
	ConfigSHA   string
}
