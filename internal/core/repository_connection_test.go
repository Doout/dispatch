package core

import "testing"

func TestCanonicalRepositoryURL(t *testing.T) {
	for _, raw := range []string{"https://GitHub.Example:8443/Acme/Builds", "https://github.example:8443/acme/builds.git/"} {
		if got, err := CanonicalRepositoryURL(raw); err != nil || got != "https://github.example:8443/acme/builds.git" {
			t.Errorf("canonical URL: %q, %v", got, err)
		}
	}
	for raw, want := range map[string]string{
		"https://GitHub.Example:443/Acme/Repo": "https://github.example/acme/repo.git",
		"https://[::1]:443/Org/Repo":           "https://[::1]/org/repo.git",
		"https://[::1]:8443/Org/Repo":          "https://[::1]:8443/org/repo.git",
	} {
		if got, err := CanonicalRepositoryURL(raw); err != nil || got != want {
			t.Errorf("canonical URL %q: got %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"git@example.test:a/b", "http://github.example/acme/repo", "https://user@github.example/acme/repo",
		"https://github.example/acme/repo?q", "https://github.example/acme/repo#", "https://github.example: /acme/repo",
		"https://github.example/../repo", "https://github.example/acme/..", "https://github.example/acme/.git",
		"https://github.example/acme%2frepo", "https://github.example/acme/repo/extra", "https://github.example//acme/repo",
	} {
		if _, err := CanonicalRepositoryURL(raw); err == nil {
			t.Errorf("accepted invalid repository URL %q", raw)
		}
	}
}
