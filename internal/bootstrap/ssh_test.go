package bootstrap

import (
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"golang.org/x/crypto/ssh"
)

func TestVerifiedSSHRejectsChangedHostBeforeAuthentication(t *testing.T) {
	var authenticated atomic.Int32
	signer, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, 32)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		config := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { authenticated.Add(1); return nil, nil }}
		config.AddHostKey(signer)
		_, _, _, _ = ssh.NewServerConn(conn, config)
	}()
	other := make([]byte, 32)
	other[0] = 1
	wrong, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(other).Public())
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	item := core.TargetBootstrap{Plan: core.TargetBootstrapPlan{SSHHost: host, SSHPort: number, SSHUser: "root", SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(wrong)))}}
	err = installSSH(context.Background(), item, SSHCredentials{Password: "must-never-be-offered"}, "approved script")
	<-done
	if err == nil || authenticated.Load() != 0 {
		t.Fatalf("changed host was offered authentication: %v count=%d", err, authenticated.Load())
	}
	if strings.Contains(err.Error(), "must-never") {
		t.Fatal("credential leaked in error")
	}
}

func TestVerifiedSSHExecutesOnlyTheApprovedInstaller(t *testing.T) {
	signer, _ := ssh.NewSignerFromKey(ed25519.NewKeyFromSeed(make([]byte, 32)))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	executed := make(chan string, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		config := &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if meta.User() != "deploy" || string(password) != "write-only" {
				t.Error("wrong authentication")
			}
			return nil, nil
		}}
		config.AddHostKey(signer)
		server, channels, requests, e := ssh.NewServerConn(conn, config)
		if e != nil {
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		ch := <-channels
		channel, commands, e := ch.Accept()
		if e != nil {
			return
		}
		defer channel.Close()
		command := <-commands
		var payload struct{ Command string }
		_ = ssh.Unmarshal(command.Payload, &payload)
		_ = command.Reply(true, nil)
		raw, _ := io.ReadAll(channel)
		executed <- payload.Command + "\n" + string(raw)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	item := core.TargetBootstrap{Plan: core.TargetBootstrapPlan{SSHHost: host, SSHPort: number, SSHUser: "deploy", SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))}}
	err = installSSH(context.Background(), item, SSHCredentials{Password: "write-only"}, "the approved installer\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := <-executed; got != "sudo -n timeout --signal=TERM --kill-after=10s 14m sh -s\nthe approved installer\n" {
		t.Fatal("unexpected remote command", got)
	}
}
