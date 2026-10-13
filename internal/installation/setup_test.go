package installation

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/instanceconnection"
)

func TestConnectionInputRejectsUnsafeAndMalformedJSONWithoutLeakingToken(t *testing.T) {
	valid := `{"controlPlaneURL":"https://doodship.test","instanceID":"instance","enrollmentToken":"private-once","remoteAccess":true}`
	for _, raw := range []string{valid + valid, valid + "junk", strings.Replace(valid, "https://doodship.test", "http://doodship.test", 1), strings.Replace(valid, "https://doodship.test", "https://user:pass@doodship.test", 1), strings.Replace(valid, "https://doodship.test", "https://doodship.test/path", 1), strings.Replace(valid, "\"remoteAccess\":true", "\"remoteAccess\":false", 1), strings.Replace(valid, "private-once", strings.Repeat("a", 1025), 1), strings.Replace(valid, "\"instanceID\":\"instance\",", "", 1), strings.Replace(valid, "remoteAccess", "unexpected", 1)} {
		_, err := ReadConnection(strings.NewReader(raw))
		if err == nil || strings.Contains(err.Error(), "private-once") {
			t.Fatalf("unexpected validation error: %v", err)
		}
	}
	if _, err := ReadConnection(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
}

func TestSetupResumesEnrollmentWithoutReplacingOrConsumingAnotherToken(t *testing.T) {
	m, c, _ := fixture(t)
	configured, setups, enrollments := false, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/status" && r.URL.Path != "/api/v1/auth/setup" {
			user, password, ok := r.BasicAuth()
			if !ok || user != "owner" || password != "private-password" {
				w.WriteHeader(401)
				return
			}
		}
		switch r.URL.Path {
		case "/api/v1/auth/status":
			json.NewEncoder(w).Encode(map[string]bool{"setupRequired": setups == 0})
		case "/api/v1/auth/setup":
			setups++
			w.WriteHeader(201)
		case "/api/v1/auth/me":
			w.Write([]byte(`{"identity":{"id":"local-owner","systemRole":"owner"}}`))
		case "/api/v1/instance-connection":
			if r.Method == "PUT" {
				var input instanceconnection.Setup
				json.NewDecoder(r.Body).Decode(&input)
				if input.OwnerUserID != "local-owner" || input.ExpectedInstanceID != "instance" || !input.RemoteAccess {
					t.Error("missing owner or instance approval")
				}
				enrollments++
				if enrollments == 1 {
					http.Error(w, "private-once", 502)
					return
				}
				configured = true
			}
			json.NewEncoder(w).Encode(instanceconnection.Status{Configured: configured, InstanceID: "instance", ControlPlaneURL: "https://doodship.test"})
		default:
			t.Error("unexpected setup request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	c.Port, _ = strconv.Atoi(port)
	saveJSON(m.path("installation.json"), c)
	m.writeCompose(c)
	saveJSON(m.path("local-owner.json"), setupOwner{UID: os.Getuid(), GID: os.Getgid(), Username: "owner", Password: "private-password", Name: c.Name, Volume: c.Volume, InstallationID: c.ID})
	original := m.Run
	m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "cp" {
			return "", os.WriteFile(args[len(args)-1], []byte("test-cli"), 0600)
		}
		return original(ctx, command, args...)
	}
	input := ConnectionInput{ControlPlaneURL: "https://doodship.test", InstanceID: "instance", EnrollmentToken: "private-once", RemoteAccess: true}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil || strings.Contains(err.Error(), input.EnrollmentToken) {
		t.Fatalf("bad failure: %v", err)
	}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if setups != 1 || enrollments != 2 {
		t.Fatalf("setup duplicated or enrollment repeated: %d %d", setups, enrollments)
	}
	input.InstanceID = "other"
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("accepted another instance")
	}
}

func TestSetupDoesNotAdoptExistingInstallationOrExposePort(t *testing.T) {
	m, c, calls := fixture(t)
	input := ConnectionInput{ControlPlaneURL: "https://doodship.test", InstanceID: "instance", EnrollmentToken: "private-once", RemoteAccess: true}
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("adopted existing installation")
	}
	c.Bind = "0.0.0.0"
	if err := m.Setup(context.Background(), c, input, false, false, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("exposed owner setup publicly")
	}
	if len(*calls) != 0 {
		t.Fatal("changed Docker before validating")
	}
}
