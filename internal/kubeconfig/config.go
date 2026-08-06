package kubeconfig

import (
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

const (
	MaxKubeconfigBytes = 4 << 20
	MaxCABytes         = 1 << 20
)

type document struct {
	CurrentContext string `yaml:"current-context"`
	Contexts       []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthority     string `yaml:"certificate-authority"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			ClientCertificate     string `yaml:"client-certificate"`
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKey             string `yaml:"client-key"`
			ClientKeyData         string `yaml:"client-key-data"`
			TokenFile             string `yaml:"tokenFile"`
		} `yaml:"user"`
	} `yaml:"users"`
}

func ValidatePath(path, requestedContext string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("kubeconfig path must be absolute")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("kubeconfig is not readable: %w", err)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, MaxKubeconfigBytes+1))
	if err != nil {
		return "", fmt.Errorf("read kubeconfig: %w", err)
	}
	return validate(contents, nil, requestedContext, false)
}

func ValidateStored(contents, certificateAuthority []byte, requestedContext string) (string, error) {
	return validate(contents, certificateAuthority, requestedContext, true)
}

func validate(contents, certificateAuthority []byte, requestedContext string, stored bool) (string, error) {
	if len(contents) == 0 {
		return "", errors.New("kubeconfig is empty")
	}
	if len(contents) > MaxKubeconfigBytes {
		return "", errors.New("kubeconfig exceeds 4 MiB")
	}
	if len(certificateAuthority) > MaxCABytes {
		return "", errors.New("CA certificate exceeds 1 MiB")
	}
	var config document
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return "", fmt.Errorf("parse kubeconfig: %w", err)
	}
	selected := strings.TrimSpace(requestedContext)
	if selected == "" {
		selected = strings.TrimSpace(config.CurrentContext)
	}
	if selected == "" {
		return "", errors.New("kubeconfig does not select a current context")
	}
	contextIndex := -1
	for index := range config.Contexts {
		if strings.TrimSpace(config.Contexts[index].Name) == selected {
			contextIndex = index
			break
		}
	}
	if contextIndex < 0 {
		return "", fmt.Errorf("kubeconfig context %q does not exist", selected)
	}
	if !stored {
		return selected, nil
	}
	contextEntry := config.Contexts[contextIndex].Context
	clusterIndex := -1
	for index := range config.Clusters {
		if strings.TrimSpace(config.Clusters[index].Name) == strings.TrimSpace(contextEntry.Cluster) {
			clusterIndex = index
			break
		}
	}
	if clusterIndex < 0 {
		return "", fmt.Errorf("kubeconfig context %q references a missing cluster", selected)
	}
	cluster := config.Clusters[clusterIndex].Cluster
	if strings.TrimSpace(cluster.Server) == "" {
		return "", fmt.Errorf("kubeconfig context %q has no cluster server", selected)
	}
	if len(certificateAuthority) > 0 {
		if !validPEMCertificates(certificateAuthority) {
			return "", errors.New("CA certificate must contain a valid PEM certificate")
		}
	} else if strings.TrimSpace(cluster.CertificateAuthorityData) != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cluster.CertificateAuthorityData))
		if err != nil || !validPEMCertificates(decoded) {
			return "", errors.New("kubeconfig contains invalid certificate-authority-data")
		}
	} else if stored && strings.TrimSpace(cluster.CertificateAuthority) != "" {
		return "", errors.New("kubeconfig references an external CA file; paste the CA certificate too")
	}
	if stored && strings.TrimSpace(contextEntry.User) != "" {
		for _, candidate := range config.Users {
			if strings.TrimSpace(candidate.Name) != strings.TrimSpace(contextEntry.User) {
				continue
			}
			if candidate.User.ClientCertificate != "" && candidate.User.ClientCertificateData == "" {
				return "", errors.New("kubeconfig references an external client certificate; embed client-certificate-data before saving")
			}
			if candidate.User.ClientKey != "" && candidate.User.ClientKeyData == "" {
				return "", errors.New("kubeconfig references an external client key; embed client-key-data before saving")
			}
			if candidate.User.TokenFile != "" {
				return "", errors.New("kubeconfig references an external token file; embed the token before saving")
			}
			break
		}
	}
	return selected, nil
}

func validPEMCertificates(contents []byte) bool {
	pool := x509.NewCertPool()
	return pool.AppendCertsFromPEM(contents)
}

// Prepare returns a kubeconfig ready for command-line clients. Stored
// credentials are materialized to a private temporary directory for the
// duration of one operation; mounted kubeconfig paths pass through unchanged.
func Prepare(config core.KubernetesServerConfig) (core.KubernetesServerConfig, func(), error) {
	if strings.TrimSpace(config.KubeconfigData) == "" {
		return config, func() {}, nil
	}
	directory, err := os.MkdirTemp("", "dispatch-kubeconfig-")
	if err != nil {
		return config, func() {}, fmt.Errorf("create kubeconfig workspace: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	contents := []byte(config.KubeconfigData)
	if strings.TrimSpace(config.CertificateAuthorityData) != "" {
		caPath := filepath.Join(directory, "ca.crt")
		if err := os.WriteFile(caPath, []byte(config.CertificateAuthorityData), 0o600); err != nil {
			cleanup()
			return config, func() {}, fmt.Errorf("write CA certificate: %w", err)
		}
		contents, err = withCertificateAuthority(contents, config.Context, caPath)
		if err != nil {
			cleanup()
			return config, func() {}, err
		}
	}
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		cleanup()
		return config, func() {}, fmt.Errorf("write kubeconfig: %w", err)
	}
	config.KubeconfigPath = path
	return config, cleanup, nil
}

func withCertificateAuthority(contents []byte, selectedContext, caPath string) ([]byte, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(contents, &raw); err != nil {
		return nil, fmt.Errorf("parse stored kubeconfig: %w", err)
	}
	clusterName := ""
	for _, item := range sequence(raw["contexts"]) {
		if text(item["name"]) != selectedContext {
			continue
		}
		clusterName = text(mapping(item["context"])["cluster"])
		break
	}
	for _, item := range sequence(raw["clusters"]) {
		if text(item["name"]) != clusterName {
			continue
		}
		cluster := mapping(item["cluster"])
		cluster["certificate-authority"] = caPath
		delete(cluster, "certificate-authority-data")
		delete(cluster, "insecure-skip-tls-verify")
		item["cluster"] = cluster
		return yaml.Marshal(raw)
	}
	return nil, fmt.Errorf("stored kubeconfig context %q has no cluster", selectedContext)
}

func sequence(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func mapping(value any) map[string]any {
	mapped, _ := value.(map[string]any)
	if mapped == nil {
		mapped = map[string]any{}
	}
	return mapped
}

func text(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}
