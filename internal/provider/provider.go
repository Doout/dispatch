package provider

import (
	"context"
	"encoding/json"
)

const APIVersion = "dispatch.provider/v1"

type Manifest struct {
	APIVersion          string          `json:"apiVersion"`
	Name                string          `json:"name"`
	DisplayName         string          `json:"displayName"`
	Version             string          `json:"version"`
	Capabilities        []string        `json:"capabilities"`
	ConfigurationSchema json.RawMessage `json:"configurationSchema"`
}

type OptionRequest struct {
	Kind   string         `json:"kind"`
	Config map[string]any `json:"config"`
}

type Option struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type CreateServerRequest struct {
	Name           string         `json:"name"`
	Region         string         `json:"region"`
	Size           string         `json:"size"`
	Image          string         `json:"image"`
	Network        string         `json:"network"`
	SSHKey         string         `json:"sshKey"`
	Bootstrap      string         `json:"bootstrap,omitempty"`
	ProviderConfig map[string]any `json:"providerConfig"`
}

type Operation struct {
	ID         string `json:"id"`
	State      string `json:"state"`
	Message    string `json:"message,omitempty"`
	ResourceID string `json:"resourceId,omitempty"`
}

type Server struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Address string            `json:"address"`
	State   string            `json:"state"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type Provider interface {
	Manifest(context.Context) (Manifest, error)
	Validate(context.Context, map[string]any) error
	Options(context.Context, OptionRequest) ([]Option, error)
	CreateServer(context.Context, string, CreateServerRequest) (Operation, error)
	Operation(context.Context, string) (Operation, error)
	Server(context.Context, string) (Server, error)
	DeleteServer(context.Context, string, string) (Operation, error)
}
