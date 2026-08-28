package workflow

import (
	"context"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestRepositoryCloneURLUsesSavedCredentialHost(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		repository string
		want       string
	}{
		{
			name:       "SSH",
			configured: "git@example.test:platform/config.git",
			repository: "platform/service",
			want:       "git@example.test:platform/service.git",
		},
		{
			name:       "HTTPS",
			configured: "https://example.test/platform/config.git",
			repository: "platform/ui",
			want:       "https://example.test/platform/ui.git",
		},
		{
			name:       "explicit clone URL",
			configured: "git@example.test:platform/config.git",
			repository: "ssh://git@other.example.test/platform/chart.git",
			want:       "ssh://git@other.example.test/platform/chart.git",
		},
	}
	service := &Service{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := service.repositoryCloneURL(context.Background(), core.ConfigSource{Repository: test.configured}, test.repository)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("repository URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseRepositoryHeadIgnoresSSHMessages(t *testing.T) {
	want := "0123456789abcdef0123456789abcdef01234567"
	output := "Warning: Permanently added 'example.test' to the list of known hosts.\n" + want + "\trefs/heads/main\n"
	got, err := parseRepositoryHead(output, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("repository head = %q, want %q", got, want)
	}
}

func TestParseRepositoryHeadRejectsWrongRef(t *testing.T) {
	_, err := parseRepositoryHead("0123456789abcdef0123456789abcdef01234567\trefs/heads/other\n", "refs/heads/main")
	if err == nil {
		t.Fatal("expected the wrong ref to be rejected")
	}
}
