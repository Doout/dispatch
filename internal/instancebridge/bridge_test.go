package instancebridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

type fixture struct {
	b      *Broker
	server *httptest.Server
	cancel context.CancelFunc
	done   chan error
}

func connectFixture(t *testing.T, remote bool, handler http.Handler, authorize func(context.Context, Operation, Identity, *http.Request) error) *fixture {
	t.Helper()
	f := &fixture{b: NewBroker(), done: make(chan error, 1)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/connection" {
			if r.Header.Get("Authorization") != "Bearer secret" {
				http.Error(w, "unauthorized", 401)
				return
			}
			f.b.ServeConnection(w, r, "a")
			return
		}
		op := OperationUser
		if r.URL.Path == "/webhook" {
			op = OperationWebhook
		}
		if r.URL.Path == "/workload" {
			op = OperationWorkload
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/edge/nodes/") {
			op = OperationNetwork
		}
		f.b.ProxyOperation(w, r, "a", op, Identity{UserID: "user-1", Role: "viewer"})
	}))
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	c, err := NewConnector(ConnectorConfig{Endpoint: f.server.URL + "/connection", Credential: "secret", Handler: handler, Authorize: authorize, RemoteAccess: remote, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	go func() { f.done <- c.Connect(ctx) }()
	wait(t, func() bool { return f.b.Connected("a") })
	t.Cleanup(func() {
		f.cancel()
		f.b.Close()
		f.server.Close()
		select {
		case <-f.done:
		case <-time.After(3 * time.Second):
			t.Error("connector did not stop")
		}
	})
	return f
}

func allow(context.Context, Operation, Identity, *http.Request) error { return nil }

func wait(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHTTPAndIdentityOverOutboundConnection(t *testing.T) {
	payload := bytes.Repeat([]byte("payload"), 600000)
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i, ok := IdentityFromContext(r.Context())
		if !ok || i.UserID != "user-1" || i.Role != "viewer" {
			t.Error("identity missing")
		}
		if OperationFromContext(r.Context()) != OperationUser {
			t.Error("operation missing")
		}
		for _, h := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Dispatch-User", "X-Doodship-Role"} {
			if r.Header.Get(h) != "" {
				t.Errorf("spoofed %s retained", h)
			}
		}
		if r.URL.Query().Get("filter") != "ok" {
			t.Error("query not preserved")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Set-Cookie", "admin=1")
		w.WriteHeader(201)
		_, _ = io.Copy(w, r.Body)
	}), allow)
	r, _ := http.NewRequest("POST", f.server.URL+"/echo?filter=ok", bytes.NewReader(payload))
	for _, h := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Dispatch-User", "X-Doodship-Role"} {
		r.Header.Set(h, "spoof")
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	actual, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 || !bytes.Equal(actual, payload) {
		t.Fatalf("streamed body mismatch: status%d bytes%d", res.StatusCode, len(actual))
	}
	if res.Header.Get("Set-Cookie") != "" {
		t.Fatal("local session cookie escaped")
	}
}

func TestRemoteAccessIsIndependentOfWebhookDelivery(t *testing.T) {
	f := connectFixture(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, string(OperationFromContext(r.Context())))
	}), allow)
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{{"/user", 403, "remote user access is disabled"}, {"/webhook", 200, "webhook"}} {
		res, err := http.Get(f.server.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status || !strings.Contains(string(body), tc.body) {
			t.Fatalf("%s: %d %s", tc.path, res.StatusCode, body)
		}
	}
}

func TestAuthorizationRunsForEveryOperation(t *testing.T) {
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("denied handler ran") }), func(context.Context, Operation, Identity, *http.Request) error { return errors.New("revoked") })
	for _, path := range []string{"/user", "/webhook"} {
		res, err := http.Get(f.server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 403 {
			t.Fatal(res.StatusCode)
		}
	}
}

func TestLiveStreamAndCancellation(t *testing.T) {
	canceled := make(chan struct{})
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: building\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}), allow)
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/logs", nil)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("data: building\n\n"))
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "data: building\n\n" {
		t.Fatal(string(buf))
	}
	cancel()
	res.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("handler retained canceled request")
	}
}

func TestDisconnectRevokesLiveRequestsAndIsolatesInstances(t *testing.T) {
	canceled := make(chan struct{})
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}), allow)
	res, err := http.Get(f.server.URL + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	other := httptest.NewRecorder()
	f.b.Proxy(other, httptest.NewRequest("GET", "/", nil), "other", Identity{UserID: "user-1"})
	if other.Code != 503 {
		t.Fatal("cross-instance connection used")
	}
	f.b.Disconnect("a")
	if f.b.Connected("a") {
		t.Fatal("revoked connection remained active")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel local handler")
	}
	_, _ = io.ReadAll(res.Body)
	res.Body.Close()
	res, err = http.Get(f.server.URL + "/next")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatal("disconnected request accepted")
	}
}

func TestSlowStreamDoesNotBlockOtherRequests(t *testing.T) {
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fast" {
			_, _ = io.WriteString(w, "fast")
			return
		}
		for r.Context().Err() == nil {
			if _, err := w.Write(bytes.Repeat([]byte("x"), chunkSize)); err != nil {
				return
			}
		}
	}), allow)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/slow", nil)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	time.Sleep(100 * time.Millisecond)
	client := &http.Client{Timeout: 2 * time.Second}
	fast, err := client.Get(f.server.URL + "/fast")
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Body.Close()
	body, _ := io.ReadAll(fast.Body)
	if string(body) != "fast" {
		t.Fatal(string(body))
	}
}

func TestReconnectAndReplacement(t *testing.T) {
	b := NewBroker()
	defer b.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { b.ServeConnection(w, r, "a") }))
	defer srv.Close()
	makeConnector := func(label string) *Connector {
		c, err := NewConnector(ConnectorConfig{Endpoint: srv.URL, Credential: "secret", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, label) }), Authorize: allow, RemoteAccess: true, AllowInsecure: true})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- makeConnector("first").Connect(ctx) }()
	wait(t, func() bool { return b.Connected("a") })
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	done2 := make(chan error, 1)
	go func() { done2 <- makeConnector("second").Connect(ctx2) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not close previous session")
	}
	rr := httptest.NewRecorder()
	b.Proxy(rr, httptest.NewRequest("GET", "/", nil), "a", Identity{UserID: "u"})
	if rr.Body.String() != "second" {
		t.Fatal(rr.Body.String())
	}
	b.Disconnect("a")
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement session did not close")
	}
	ctx3, cancel3 := context.WithCancel(context.Background())
	done3 := make(chan error, 1)
	go func() { done3 <- makeConnector("third").Run(ctx3) }()
	wait(t, func() bool { return b.Connected("a") })
	b.Disconnect("a")
	wait(t, func() bool { return b.Connected("a") })
	cancel3()
	select {
	case <-done3:
	case <-time.After(3 * time.Second):
		t.Fatal("reconnect loop did not stop")
	}
}

func TestConfigurationRequiresTLSAndAuthorization(t *testing.T) {
	for _, endpoint := range []string{"http://control.example/ws", "ftp://control.example/ws", "https://user:pass@control.example/ws", "https://control.example/ws#fragment"} {
		if _, err := NewConnector(ConnectorConfig{Endpoint: endpoint, Credential: "x", Handler: http.NotFoundHandler(), Authorize: allow}); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	if _, err := NewConnector(ConnectorConfig{Endpoint: "https://control.example/ws", Credential: "x", Handler: http.NotFoundHandler()}); err == nil {
		t.Fatal("missing authorization accepted")
	}
}

func TestConcurrentRequests(t *testing.T) {
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.URL.Path) }), allow)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/request-%d", i)
			res, err := http.Get(f.server.URL + path)
			if err != nil {
				t.Error(err)
				return
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if string(body) != path {
				t.Errorf("request mixed: %s", body)
			}
		}()
	}
	wg.Wait()
}

func TestWorkloadPreservesAppCredentialsWithoutPlatformCookies(t *testing.T) {
	f := connectFixture(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if OperationFromContext(r.Context()) != OperationWorkload {
			t.Error("wrong operation")
		}
		if r.Host == "" {
			t.Error("workload host missing")
		}
		if r.Header.Get("Authorization") != "Bearer app-token" {
			t.Error("app authorization lost")
		}
		if r.Header.Get("X-Dispatch-Workload-Host") != "" {
			t.Error("spoofed internal header retained")
		}
		if c, err := r.Cookie("app_session"); err != nil || c.Value != "app" {
			t.Error("app cookie lost")
		}
		for _, name := range []string{"__Host-dispatch-session", "__Host-doodship-managed-login", "dispatch_session"} {
			if _, err := r.Cookie(name); err == nil {
				t.Errorf("platform cookie retained %s", name)
			}
		}
		w.Header().Add("Set-Cookie", "app_session=new; Secure; HttpOnly")
		w.Header().Add("Set-Cookie", "__Host-dispatch-session=stolen; Secure")
		_, _ = io.WriteString(w, "app")
	}), allow)
	r, _ := http.NewRequest("GET", f.server.URL+"/workload", nil)
	r.Header.Set("Authorization", "Bearer app-token")
	r.Header.Set("Cookie", "app_session=app; __Host-dispatch-session=platform; __Host-doodship-managed-login=platform; dispatch_session=platform")
	r.Header.Set("X-Dispatch-Workload-Host", "evil.example")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "app" {
		t.Fatal(string(body))
	}
	if got := res.Header.Values("Set-Cookie"); len(got) != 1 || !strings.HasPrefix(got[0], "app_session=") {
		t.Fatal(got)
	}
}

func TestWorkloadRejectsInvalidHostAndUpgrades(t *testing.T) {
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid request reached backend") }), allow)
	for _, host := range []string{"", strings.Repeat("a", 254), "evil.example/path", "evil.example\r\nInjected: value", "https://evil.example"} {
		r := httptest.NewRequest("GET", "/app", nil)
		r.Host = host
		w := httptest.NewRecorder()
		f.b.ProxyOperation(w, r, "a", OperationWorkload, Identity{})
		if w.Code != 400 {
			t.Fatalf("invalid host %q accepted", host)
		}
	}
	r := httptest.NewRequest("GET", "/app", nil)
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	f.b.ProxyOperation(w, r, "a", OperationWorkload, Identity{})
	if w.Code != 400 {
		t.Fatal("workload websocket upgrade accepted")
	}
}

func TestProtocolVersionIsRequired(t *testing.T) {
	b := NewBroker()
	defer b.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { b.ServeConnection(w, r, "a") }))
	defer srv.Close()
	for _, protocol := range []string{"", "other-v1"} {
		cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(srv.URL, "http"), srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		if protocol != "" {
			cfg.Protocol = []string{protocol}
		}
		ws, err := cfg.DialContext(context.Background())
		if err == nil {
			ws.Close()
			t.Fatal("unsupported protocol accepted")
		}
	}
}

func TestNetworkRoutesAreNarrowAndPreserveOnlyNodeCredentials(t *testing.T) {
	allowed := []struct{ method, path string }{
		{"GET", "/api/v1/github-checks/report-1/logs"},
		{"POST", "/api/v1/edge/nodes/node-1/enroll"},
		{"POST", "/api/v1/edge/nodes/node-1/challenge"},
		{"POST", "/api/v1/edge/nodes/node-1/session"},
		{"GET", "/api/v1/edge/nodes/node-1/jobs/next"},
		{"POST", "/api/v1/edge/nodes/node-1/jobs/job-1/complete"},
		{"GET", "/api/v1/edge/nodes/node-1/runtime/jobs/next"},
		{"POST", "/api/v1/edge/nodes/node-1/runtime/jobs/job-1/heartbeat"},
		{"POST", "/api/v1/edge/nodes/node-1/runtime/jobs/job-1/complete"},
	}
	for _, tc := range allowed {
		if !NetworkRequestAllowed(httptest.NewRequest(tc.method, tc.path, nil)) {
			t.Fatalf("valid node route rejected: %s", tc.path)
		}
		if NetworkRequestAllowed(httptest.NewRequest(http.MethodDelete, tc.path, nil)) {
			t.Fatalf("incorrect method accepted: %s", tc.path)
		}
	}
	for _, path := range []string{"/api/v1/github-checks/report/logs/extra", "/api/v1/github-checks/%72eport/logs", "/api/v1/github-checks/../logs", "/api/v1/edge/nodes", "/api/v1/account/login", "/api/v1/edge/nodes/node-1/workflow/jobs/next", "/api/v1/edge/nodes/node-1/jobs/job-1/cancel", "/api/v1/edge/nodes/node-1/runtime/jobs/job-1/complete/extra", "/api/v1/edge/nodes/%6eode-1/session", "/api/v1/edge/nodes/../session", "/api/v1/edge/nodes//session"} {
		if NetworkRequestAllowed(httptest.NewRequest("POST", path, nil)) {
			t.Fatal("network route escaped allowlist: " + path)
		}
	}
	f := connectFixture(t, false, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if OperationFromContext(r.Context()) != OperationNetwork {
			t.Error("wrong network operation")
		}
		if _, ok := IdentityFromContext(r.Context()); ok {
			t.Error("node operation inherited human identity")
		}
		if r.Header.Get("Authorization") != "Bearer node-token" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Dispatch-User") != "" {
			t.Error("incorrect node credentials")
		}
		w.Header().Set("Set-Cookie", "admin=1")
		_, _ = io.WriteString(w, "node")
	}), allow)
	r, _ := http.NewRequest("GET", f.server.URL+"/api/v1/edge/nodes/node-1/jobs/next", nil)
	r.Header.Set("Authorization", "Bearer node-token")
	r.Header.Set("Cookie", "session=human")
	r.Header.Set("X-Dispatch-User", "admin")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "node" || res.Header.Get("Set-Cookie") != "" {
		t.Fatal("network credentials changed")
	}
}

func TestDisconnectInterruptsIncompleteUpload(t *testing.T) {
	started := make(chan struct{})
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); _, _ = io.Copy(io.Discard, r.Body) }), allow)
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	r, _ := http.NewRequest("POST", f.server.URL+"/upload", reader)
	done := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(r)
		if res != nil {
			res.Body.Close()
		}
		done <- err
	}()
	if _, err := writer.Write([]byte("incomplete upload")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	f.b.Disconnect("a")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("revocation retained blocked upload")
	}
}
