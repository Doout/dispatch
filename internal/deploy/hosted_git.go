package deploy

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

// PrepareIsolatedGitEnvironment gives hosted source reads only the credential
// supplied by this tenant. The controller's environment, Git configuration,
// credential helpers, SSH configuration and agent are never inherited.
func PrepareIsolatedGitEnvironment(app core.App) ([]string, func(), error) {
	directory, err := os.MkdirTemp("", "dispatch-hosted-git-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	templates := filepath.Join(directory, "templates")
	if err = os.Mkdir(templates, 0700); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	environment := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + directory, "XDG_CONFIG_HOME=" + directory, "LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=https:ssh", "GIT_TEMPLATE_DIR=" + templates,
	}
	config := [][2]string{{"credential.helper", ""}, {"core.hooksPath", os.DevNull}, {"http.followRedirects", "false"}}
	ssh := "ssh -F /dev/null -o BatchMode=yes -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=accept-new -o GlobalKnownHostsFile=/dev/null -o PermitLocalCommand=no -o ProxyCommand=none -o ProxyJump=none -o UserKnownHostsFile=" + gitShellQuote(filepath.Join(directory, "known_hosts"))
	credential := strings.TrimSpace(app.SourceCredential)
	switch app.SourceAuthType {
	case "":
		if credential != "" {
			cleanup()
			return nil, func() {}, errors.New("repository credential requires an explicit authentication type")
		}
		ssh += " -o IdentityFile=none"
	case SourceAuthGitHubApp, SourceAuthGitHubToken:
		if credential == "" {
			cleanup()
			return nil, func() {}, errors.New("configured source credential is empty")
		}
		header := "Authorization: Bearer " + credential
		if app.SourceAuthType == SourceAuthGitHubApp {
			header = "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+credential))
		}
		config = append(config, [2]string{"http.extraHeader", header})
		ssh += " -o IdentityFile=none"
	case SourceAuthSSHKey:
		if credential == "" {
			cleanup()
			return nil, func() {}, errors.New("configured source credential is empty")
		}
		path := filepath.Join(directory, "identity")
		if err = os.WriteFile(path, []byte(credential+"\n"), 0600); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		ssh += " -i " + gitShellQuote(path)
	default:
		cleanup()
		return nil, func() {}, errors.New("unsupported repository authentication type")
	}
	environment = append(environment, "GIT_SSH_COMMAND="+ssh, "GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for index, entry := range config {
		environment = append(environment, "GIT_CONFIG_KEY_"+strconv.Itoa(index)+"="+entry[0], "GIT_CONFIG_VALUE_"+strconv.Itoa(index)+"="+entry[1])
	}
	return environment, cleanup, nil
}

func gitShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
