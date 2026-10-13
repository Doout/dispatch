package installation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instanceconnection"
)

// ConnectionInput travels over stdin, never Docker arguments or environment.
type ConnectionInput struct {
	InstanceID      string `json:"instanceID"`
	ControlPlaneURL string `json:"controlPlaneURL"`
	EnrollmentToken string `json:"enrollmentToken"`
	DirectURL       string `json:"directURL"`
	RemoteAccess    bool   `json:"remoteAccess"`
}

func ReadConnection(in io.Reader) (ConnectionInput, error) {
	var input ConnectionInput
	raw, err := io.ReadAll(io.LimitReader(in, 8193))
	if err != nil || len(raw) > 8192 {
		return input, errors.New("connection JSON must be at most 8 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, errors.New("provide connection JSON on standard input")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return input, errors.New("provide one connection JSON object")
	}
	return input, validateConnection(input)
}

func validateConnection(input ConnectionInput) error {
	if input.InstanceID == "" || len(input.InstanceID) > 256 {
		return errors.New("an instance ID is required")
	}
	u, err := url.Parse(input.ControlPlaneURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("control plane address must be an HTTPS origin")
	}
	if input.EnrollmentToken == "" || len(input.EnrollmentToken) > 1024 || strings.ContainsAny(input.EnrollmentToken, "\r\n") {
		return errors.New("a one-use connection token is required")
	}
	if input.DirectURL != "" {
		direct, err := url.Parse(input.DirectURL)
		if err != nil || (direct.Scheme != "https" && direct.Scheme != "http") || direct.Hostname() == "" || direct.User != nil || direct.RawQuery != "" || direct.Fragment != "" {
			return errors.New("instance address must be an HTTP or HTTPS URL without credentials")
		}
	}
	if !input.RemoteAccess && input.DirectURL == "" {
		return errors.New("provide an instance address or enable remote access")
	}
	return nil
}

type setupOwner struct {
	UID            int    `json:"uid"`
	GID            int    `json:"gid"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	Name           string `json:"name"`
	Volume         string `json:"volume"`
	InstallationID string `json:"installationID,omitempty"`
}

// Setup resumes only installations created by this flow. Existing self-hosted
// installations connect through their owner-approved Connections page instead.
func (m *Manager) Setup(ctx context.Context, c Config, input ConnectionInput, pull, automatic bool, uid, gid int) (resultErr error) {
	if uid < 0 || gid < 0 {
		return errors.New("host UID and GID must be nonnegative")
	}
	if err := validateConnection(input); err != nil {
		return err
	}
	if net.ParseIP(c.Bind) == nil || !net.ParseIP(c.Bind).IsLoopback() {
		return errors.New("setup must bind to a loopback address; configure a reverse proxy after connecting")
	}
	var owner setupOwner
	raw, err := os.ReadFile(m.path("local-owner.json"))
	if err == nil {
		info, e := os.Lstat(m.path("local-owner.json"))
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("local-owner.json must be a private regular file")
		}
		if json.Unmarshal(raw, &owner) != nil || owner.Password == "" || owner.Name != c.Name || owner.Volume != c.Volume || owner.UID != uid || owner.GID != gid {
			return errors.New("setup belongs to a different installation")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if _, e := os.Lstat(m.path("installation.json")); !errors.Is(e, os.ErrNotExist) {
			return errors.New("existing installations must connect through Settings > Connections")
		}
		password := make([]byte, 24)
		if _, err = rand.Read(password); err != nil {
			return err
		}
		owner = setupOwner{UID: uid, GID: gid, Username: "doodship-owner", Password: hex.EncodeToString(password), Name: c.Name, Volume: c.Volume}
		if err = saveJSON(m.path("local-owner.json"), owner); err != nil {
			return err
		}
	} else {
		return err
	}

	defer func() { resultErr = errors.Join(resultErr, m.ownSetupFiles(owner)) }()
	installed, err := m.Load()
	if err != nil {
		if _, e := os.Stat(m.path("installation.json")); !errors.Is(e, os.ErrNotExist) {
			return err
		}
		if err = m.Install(ctx, c, "", pull); err != nil {
			return err
		}
		installed, err = m.Load()
		if err != nil {
			return err
		}
	} else {
		if installed.Name != owner.Name || installed.Volume != owner.Volume || (owner.InstallationID != "" && installed.ID != owner.InstallationID) {
			return errors.New("setup installation identity changed")
		}
		if err = m.preflight(ctx, installed.Socket); err != nil {
			return err
		}
		if err = m.checkManaged(ctx, installed); err != nil {
			return err
		}
		if err = m.waitHealthy(ctx, installed); err != nil {
			if err = m.Recover(ctx); err != nil {
				return err
			}
		}
	}
	owner.InstallationID = installed.ID
	if err = saveJSON(m.path("local-owner.json"), owner); err != nil {
		return err
	}
	if err = m.syncCLI(ctx, installed); err != nil {
		return err
	}
	if err = m.connectInstallation(ctx, installed, owner, input); err != nil {
		return fmt.Errorf("server installed; retry setup with a current connection token: %w", err)
	}
	if automatic {
		if err = m.AutoUpgrade(ctx, "enable", 24*time.Hour, pull, ""); err != nil {
			return fmt.Errorf("server connected; automatic updates could not start: %w", err)
		}
	}
	fmt.Fprintf(m.Out, "Connected to Doodship. Installation: %s\nCLI: %s\nLocal recovery login: %s\n", m.Dir, m.path("bin/dispatch"), m.path("local-owner.json"))
	return nil
}

func (m *Manager) connectInstallation(ctx context.Context, c Config, owner setupOwner, input ConnectionInput) error {
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "http://" + net.JoinHostPort(c.Bind, fmt.Sprint(c.Port)) + "/api/v1"
	request := func(method, path string, body any, authenticated bool, result any) (int, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(raw))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		if authenticated {
			req.SetBasicAuth(owner.Username, owner.Password)
		}
		response, err := client.Do(req)
		if err != nil {
			return 0, errors.New("cannot reach the local controller")
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return response.StatusCode, fmt.Errorf("local controller rejected %s %s (HTTP %d)", method, path, response.StatusCode)
		}
		if result != nil {
			err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(result)
		}
		return response.StatusCode, err
	}
	var auth struct {
		SetupRequired bool `json:"setupRequired"`
	}
	if _, err := request("GET", "/auth/status", nil, false, &auth); err != nil {
		return err
	}
	if auth.SetupRequired {
		if _, err := request("POST", "/auth/setup", map[string]string{"username": owner.Username, "password": owner.Password}, false, nil); err != nil {
			return err
		}
	}
	var me struct {
		Identity core.Identity `json:"identity"`
	}
	if _, err := request("GET", "/auth/me", nil, true, &me); err != nil {
		return err
	}
	if me.Identity.ID == "" || me.Identity.SystemRole != core.UserRoleOwner || me.Identity.ID == "controller-owner" {
		return errors.New("setup requires a local owner account")
	}
	var status instanceconnection.Status
	if _, err := request("GET", "/instance-connection", nil, true, &status); err != nil {
		return err
	}
	if status.Configured {
		if status.InstanceID != input.InstanceID || status.ControlPlaneURL != strings.TrimRight(input.ControlPlaneURL, "/") {
			return errors.New("server is already connected to another control plane")
		}
		// A successful enrollment survives retries without consuming another token.
		return nil
	}
	_, err := request("PUT", "/instance-connection", instanceconnection.Setup{ExpectedInstanceID: input.InstanceID, ControlPlaneURL: input.ControlPlaneURL, EnrollmentToken: input.EnrollmentToken, DirectURL: input.DirectURL, RemoteAccess: input.RemoteAccess, OwnerUserID: me.Identity.ID}, true, &status)
	if err != nil {
		return err
	}
	if status.InstanceID != input.InstanceID {
		return errors.New("connection token belongs to another instance; disconnect in the local Connections page")
	}
	if !status.Configured {
		return errors.New("controller did not save the connection")
	}
	return nil
}

// Copy the CLI from the pinned controller image so it matches the installation.
// It stays in the home installation directory, outside the controller volume.
func (m *Manager) syncCLI(ctx context.Context, c Config) error {
	if err := os.MkdirAll(m.path("bin"), 0700); err != nil {
		return err
	}
	id, err := m.container(ctx, c)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(m.Dir, ".cli-*")
	if err != nil {
		return err
	}
	temp.Close()
	defer os.Remove(temp.Name())
	if _, err = m.Run(ctx, "docker", "cp", id+":/usr/local/bin/dispatch", temp.Name()); err != nil {
		return err
	}
	raw, err := os.ReadFile(temp.Name())
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return errors.New("controller CLI is empty")
	}
	if err = writeFile(filepath.Join(m.Dir, "bin", "dispatch"), raw, 0755); err != nil {
		return err
	}
	var owner setupOwner
	if raw, e := os.ReadFile(m.path("local-owner.json")); e == nil && json.Unmarshal(raw, &owner) == nil {
		return m.ownSetupFiles(owner)
	}
	return nil
}

func (m *Manager) ownSetupFiles(owner setupOwner) error {
	for _, name := range []string{"", ".lock", "local-owner.json", "installation.json", "compose.json", "dispatch.env", "automatic.json", "bin", "bin/dispatch"} {
		path := m.path(name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return errors.New("setup files must not be symbolic links")
		}
		if err = os.Chown(path, owner.UID, owner.GID); err != nil {
			return fmt.Errorf("set installation file ownership: %w", err)
		}
	}
	return nil
}
