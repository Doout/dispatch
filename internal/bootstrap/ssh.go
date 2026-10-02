package bootstrap

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"golang.org/x/crypto/ssh"
)

func sshAuth(input SSHCredentials) (ssh.AuthMethod, error) {
	if len(input.Password) > 8192 || len(input.PrivateKey) > 65536 || len(input.PrivateKeyPassword) > 8192 {
		return nil, errors.New("SSH credential exceeds its limit")
	}
	if input.Password != "" && input.PrivateKey != "" {
		return nil, errors.New("choose one SSH authentication method")
	}
	if input.Password != "" {
		return ssh.Password(input.Password), nil
	}
	if input.PrivateKey == "" {
		return nil, errors.New("provide a write-only SSH password or private key")
	}
	var signer ssh.Signer
	var err error
	if input.PrivateKeyPassword != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(input.PrivateKey), []byte(input.PrivateKeyPassword))
	} else {
		signer, err = ssh.ParsePrivateKey([]byte(input.PrivateKey))
	}
	if err != nil {
		return nil, errors.New("the SSH private key cannot be parsed")
	}
	return ssh.PublicKeys(signer), nil
}

// installSSH checks the pinned host key during key exchange, before the SSH
// library offers authentication. Only the generated installer can be executed.
func installSSH(ctx context.Context, item core.TargetBootstrap, credentials SSHCredentials, script string) error {
	expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(item.Plan.SSHHostKey))
	if err != nil {
		return errors.New("the reviewed SSH host key is invalid")
	}
	auth, err := sshAuth(credentials)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", sshAddress(item.Plan))
	if err != nil {
		return errors.New("SSH connection is unavailable; verify the reviewed host and port")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	stopped := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopped()
	config := &ssh.ClientConfig{User: item.Plan.SSHUser, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: func(_ string, _ net.Addr, actual ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(expected.Marshal(), actual.Marshal()) != 1 {
			return errors.New("SSH host key differs from the reviewed key")
		}
		return nil
	}}
	connection, channels, requests, err := ssh.NewClientConn(conn, sshAddress(item.Plan), config)
	if err != nil {
		return errors.New("SSH host verification or authentication failed; no installer was sent")
	}
	client := ssh.NewClient(connection, channels, requests)
	defer client.Close()
	_ = conn.SetDeadline(time.Time{})
	session, err := client.NewSession()
	if err != nil {
		return errors.New("cannot open the verified installation session")
	}
	defer session.Close()
	session.Stdin = strings.NewReader(script)
	session.Stdout = io.Discard
	session.Stderr = io.Discard
	command := "timeout --signal=TERM --kill-after=10s 14m sh -s"
	if item.Plan.SSHUser != "root" {
		command = "sudo -n timeout --signal=TERM --kill-after=10s 14m sh -s"
	}
	if err = session.Run(command); err != nil {
		return errors.New("approved installation did not finish; retry the same operation after checking prerequisites")
	}
	return nil
}
func (m *Manager) InstallSSH(ctx context.Context, id, digest string) (core.TargetBootstrap, error) {
	m.mu.Lock()
	item, err := m.Store.GetTargetBootstrap(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return item, err
	}
	if item.Digest != digest || item.AcceptedAt == nil || item.Plan.Method != "ssh" || item.State == "cancelled" || !item.ClaimExpiresAt.After(m.now()) {
		m.mu.Unlock()
		return item, ErrConflict
	}
	if item.State == "ready" {
		m.mu.Unlock()
		return item, nil
	}
	if item.LeaseUntil != nil && item.LeaseUntil.After(m.now()) {
		m.mu.Unlock()
		return item, errors.New("installation is already running")
	}
	if _, err = m.binding(ctx, item); err != nil {
		m.mu.Unlock()
		return item, err
	}
	generation := item.ExpectedGeneration
	if item.Generation > 0 {
		generation = item.Generation
	}
	credential, readErr := m.Store.GetEdgeCredential(ctx, item.NodeID)
	if readErr != nil && !errors.Is(readErr, store.ErrNotFound) || credential.Generation != generation {
		m.mu.Unlock()
		return item, ErrConflict
	}
	input, err := m.inputs(item)
	if err != nil {
		m.mu.Unlock()
		return item, err
	}
	script, err := renderScript(item, input.ClaimToken)
	if err != nil {
		m.mu.Unlock()
		return item, err
	}
	expiry := m.now().Add(16 * time.Minute)
	item.LeaseUntil = &expiry
	item.State = "installing"
	item.InstallationState = "installing"
	item.Message = "Connecting with the verified host key and executing the approved installer."
	if err = m.save(ctx, &item); err != nil {
		m.mu.Unlock()
		return item, err
	}
	m.mu.Unlock()
	installer := m.SSHInstall
	if installer == nil {
		installer = installSSH
	}
	err = installer(ctx, item, input.SSH, script)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, loadErr := m.Store.GetTargetBootstrap(ctx, id)
	if loadErr != nil {
		return current, loadErr
	}
	current.LeaseUntil = nil
	if current.State == "ready" {
		return current, m.save(ctx, &current)
	}
	if err != nil {
		current.State = "unknown"
		current.InstallationState = "interrupted"
		current.Message = "Installation did not complete. Verify the host key and prerequisites, then retry this operation; no new machine will be allocated."
	} else {
		current.State = "waiting"
		current.InstallationState = "installed"
		current.Message = "Agent installed; waiting for enrolled identity and runtime readiness."
	}
	saveErr := m.save(ctx, &current)
	if saveErr != nil {
		return current, saveErr
	}
	return current, err
}
