package core

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RepositoryConnection binds a project workflow's connection name to read access for one repository.
type RepositoryConnection struct {
	ID                 string    `json:"id"`
	ProjectID          string    `json:"projectId"`
	Name               string    `json:"name"`
	RepositoryURL      string    `json:"repositoryUrl"`
	GitHubAppID        string    `json:"githubAppId,omitempty"`
	CredentialSecretID string    `json:"credentialSecretId,omitempty"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

var repositoryURLPart = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// CanonicalRepositoryURL is the exact HTTPS clone URL to which a named credential may be sent.
func CanonicalRepositoryURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	invalid := errors.New("enter an HTTPS repository URL with the host, owner, and repository name")
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		strings.HasSuffix(u.Host, ":") || strings.ContainsAny(raw, "%?#\\") {
		return "", invalid
	}
	owner, name, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/"), "/")
	name = strings.TrimSuffix(strings.ToLower(name), ".git")
	if !ok || !repositoryURLPart.MatchString(owner) || !repositoryURLPart.MatchString(name) ||
		owner == "." || owner == ".." || name == "." || name == ".." {
		return "", invalid
	}
	return "https://" + RepositoryHostPort(u) + "/" + strings.ToLower(owner+"/"+name) + ".git", nil
}

// RepositoryHostPort identifies a Git host, treating an explicit HTTPS default port as implicit.
func RepositoryHostPort(u *url.URL) string {
	host := strings.ToLower(u.Host)
	port := u.Port()
	if u.Scheme == "https" && strings.TrimLeft(port, "0") == "443" {
		return strings.TrimSuffix(host, ":"+port)
	}
	return host
}
