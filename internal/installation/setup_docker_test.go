package installation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real Docker CLI/bootstrap against a disposable TLS control plane.
// No production account, instance, image tag, port or volume is used.
func TestConnectedDockerSetup(t *testing.T) {
	image := os.Getenv("DISPATCH_INSTALLATION_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_INSTALLATION_DOCKER_IMAGE to a local controller image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	name := "setup-" + suffix
	m := New(t.TempDir())
	os.Chmod(m.Dir, 0700)
	m.Out = io.Discard
	network := name + "-net"
	plane := name + "-plane"
	if _, err := m.Run(ctx, "docker", "network", "create", network); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		m.Run(cleanup, "docker", "network", "rm", network)
	})
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable setup test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{plane}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	os.WriteFile(filepath.Join(scratch, "test-ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	trustedImage := "dispatch-setup-ca:" + suffix
	os.WriteFile(filepath.Join(scratch, "Containerfile"), []byte("FROM "+image+"\nUSER 0\nCOPY test-ca.crt /usr/local/share/ca-certificates/setup-test.crt\nRUN update-ca-certificates\nUSER dispatch\n"), 0600)
	if _, err = m.Run(ctx, "docker", "build", "-f", filepath.Join(scratch, "Containerfile"), "-t", trustedImage, scratch); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(scratch, "test-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
	previous := os.Getenv("CGO_ENABLED")
	os.Setenv("CGO_ENABLED", "0")
	_, err = m.Run(ctx, "go", "build", "-o", filepath.Join(scratch, "controlplane"), "./testdata/controlplane")
	os.Setenv("CGO_ENABLED", previous)
	if err != nil {
		t.Fatal(err)
	}
	cpURL := "https://" + plane + ":8443"
	if _, err = m.Run(ctx, "docker", "run", "--detach", "--name", plane, "--network", network, "--user", "0", "--volume", scratch+":/fixtures:ro", "--entrypoint", "/fixtures/controlplane", trustedImage, cpURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		m.Run(cleanup, "docker", "rm", "-f", plane)
	})
	fixtureStatus := func() (bool, int32) {
		raw, err := m.Run(ctx, "docker", "exec", plane, "/fixtures/controlplane", cpURL, "status")
		if err != nil {
			return false, 0
		}
		var status struct {
			Connected   bool  `json:"connected"`
			Enrollments int32 `json:"enrollments"`
		}
		json.Unmarshal([]byte(raw), &status)
		return status.Connected, status.Enrollments
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	c := Config{Name: name, Volume: name + "-data", Image: trustedImage, Socket: "/var/run/docker.sock", Bind: "127.0.0.1", Port: port, ProxyNetwork: network, Hostname: "setup-fixture.invalid"}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		m.Run(cleanup, "docker", "rm", "-f", name+"-updater")
		m.compose(cleanup, c, "down", "--remove-orphans")
		if installed, e := m.Load(); e == nil && m.verifyVolume(cleanup, installed) == nil {
			m.Run(cleanup, "docker", "volume", "rm", c.Volume)
		}
		m.Run(cleanup, "docker", "image", "rm", trustedImage)
	})
	// Run the shipped CLI in a one-shot container, with the same host paths as UI setup.
	payload, _ := json.Marshal(ConnectionInput{ControlPlaneURL: cpURL, InstanceID: "instance", EnrollmentToken: "disposable-once", RemoteAccess: true})
	inputFile := m.path("test-input.json")
	os.WriteFile(inputFile, payload, 0600)
	command := []string{"run", "--rm", "--user", "0", "--network", "host", "--volume", m.Dir + ":" + m.Dir, "--volume", "/var/run/docker.sock:/var/run/docker.sock", "--entrypoint", "sh", trustedImage, "-c", `exec dispatch setup --dir "$1" --name "$2" --port "$3" --image "$4" --pull=false --proxy-network "$7" --hostname setup-fixture.invalid --host-uid "$5" --host-gid "$6" --connection-stdin < "$1/test-input.json"`, "setup", m.Dir, name, fmt.Sprint(port), trustedImage, fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid()), network}
	if _, err = m.Run(ctx, "docker", command...); err != nil {
		t.Fatal(err)
	}
	os.Remove(inputFile)
	deadline := time.Now().Add(15 * time.Second)
	connected, _ := fixtureStatus()
	for !connected && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		connected, _ = fixtureStatus()
	}
	if !connected {
		t.Fatal("outbound WebSocket did not connect")
	}
	// Resuming uses persisted credentials, not a second enrollment.
	os.WriteFile(inputFile, payload, 0600)
	if _, err = m.Run(ctx, "docker", command...); err != nil {
		t.Fatal(err)
	}
	os.Remove(inputFile)
	if _, enrollments := fixtureStatus(); enrollments != 1 {
		t.Fatal("enrollment repeated during resume")
	}
	installed, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.waitHealthy(ctx, installed); err != nil {
		t.Fatal(err)
	}
	if id, err := m.updater(ctx, installed); err != nil || id == "" {
		t.Fatal("Docker updater missing", err)
	}
	binary := m.path("bin/dispatch")
	if version, err := m.Run(ctx, binary, "version"); err != nil || !strings.Contains(version, "Dispatch") {
		t.Fatal("installed binary cannot run", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		_, err = m.Run(ctx, binary, "status")
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "another installation command is running") || time.Now().After(deadline) {
			t.Fatal("installed CLI did not find home configuration", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Read the controller's private grant through a helper, with no credentials in logs.
	ownerRaw, err := os.ReadFile(m.path("local-owner.json"))
	if err != nil {
		t.Fatal(err)
	}
	var owner setupOwner
	json.Unmarshal(ownerRaw, &owner)
	req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/v1/instance-connection", port), nil)
	req.SetBasicAuth(owner.Username, owner.Password)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status struct {
		Configured   bool `json:"configured"`
		UserMappings []struct {
			Subject string `json:"subject"`
		} `json:"userMappings"`
	}
	json.NewDecoder(response.Body).Decode(&status)
	if !status.Configured || len(status.UserMappings) != 1 || status.UserMappings[0].Subject != "cp-owner" {
		t.Fatal("owner mapping missing")
	}
}
