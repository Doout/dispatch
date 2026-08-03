package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Server struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	Runtime   string    `json:"runtime"`
	State     string    `json:"state"`
	AgentMode string    `json:"agentMode"`
	CreatedAt time.Time `json:"createdAt"`
}

type BuildType string

const (
	BuildTypeDockerfile BuildType = "dockerfile"
	BuildTypeCompose    BuildType = "compose"
)

type App struct {
	ID             string    `json:"id"`
	ProjectID      string    `json:"projectId"`
	ServerID       string    `json:"serverId"`
	Name           string    `json:"name"`
	SourceRepo     string    `json:"sourceRepo"`
	Branch         string    `json:"branch"`
	BuildType      BuildType `json:"buildType"`
	ContextPath    string    `json:"contextPath"`
	DockerfilePath string    `json:"dockerfilePath"`
	ComposePath    string    `json:"composePath"`
	ContainerPort  int       `json:"containerPort"`
	Domain         string    `json:"domain"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (a App) SpecDigest() string {
	payload, _ := json.Marshal(struct {
		ServerID       string
		SourceRepo     string
		Branch         string
		BuildType      BuildType
		ContextPath    string
		DockerfilePath string
		ComposePath    string
		ContainerPort  int
		Domain         string
	}{a.ServerID, a.SourceRepo, a.Branch, a.BuildType, a.ContextPath, a.DockerfilePath, a.ComposePath, a.ContainerPort, a.Domain})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type DeploymentState string

const (
	DeploymentQueued    DeploymentState = "queued"
	DeploymentFetching  DeploymentState = "fetching"
	DeploymentBuilding  DeploymentState = "building"
	DeploymentStarting  DeploymentState = "starting"
	DeploymentChecking  DeploymentState = "checking"
	DeploymentRouting   DeploymentState = "routing"
	DeploymentSucceeded DeploymentState = "succeeded"
	DeploymentFailed    DeploymentState = "failed"
	DeploymentCancelled DeploymentState = "cancelled"
)

func (s DeploymentState) Terminal() bool {
	return s == DeploymentSucceeded || s == DeploymentFailed || s == DeploymentCancelled
}

type Deployment struct {
	ID         string          `json:"id"`
	AppID      string          `json:"appId"`
	CommitSHA  string          `json:"commitSha"`
	SpecDigest string          `json:"specDigest"`
	State      DeploymentState `json:"state"`
	Message    string          `json:"message"`
	CreatedAt  time.Time       `json:"createdAt"`
	StartedAt  *time.Time      `json:"startedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	LeaseUntil *time.Time      `json:"leaseUntil,omitempty"`
	App        *App            `json:"app,omitempty"`
	Server     *Server         `json:"server,omitempty"`
}

type DeploymentLog struct {
	ID           int64     `json:"id"`
	DeploymentID string    `json:"deploymentId"`
	Level        string    `json:"level"`
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Overview struct {
	Demo        bool         `json:"demo"`
	Projects    []Project    `json:"projects"`
	Servers     []Server     `json:"servers"`
	Apps        []App        `json:"apps"`
	Deployments []Deployment `json:"deployments"`
}
