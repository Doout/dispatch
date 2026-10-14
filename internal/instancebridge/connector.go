package instancebridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type ConnectorConfig struct {
	Endpoint       string
	Credential     string
	Handler        http.Handler
	Authorize      func(context.Context, Operation, Identity, *http.Request) error
	RemoteAccess   bool
	OnConnected    func()
	OnDisconnected func()
	// AllowInsecure is intended for loopback development and tests only.
	AllowInsecure bool
}

type Connector struct {
	config   ConnectorConfig
	endpoint string
	origin   string
}

func NewConnector(cfg ConnectorConfig) (*Connector, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid control-plane endpoint")
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		if !cfg.AllowInsecure {
			return nil, errors.New("control-plane connection requires TLS")
		}
		u.Scheme = "ws"
	default:
		return nil, errors.New("invalid control-plane endpoint scheme")
	}
	if cfg.Credential == "" || strings.ContainsAny(cfg.Credential, "\r\n") || cfg.Handler == nil || cfg.Authorize == nil {
		return nil, errors.New("connection credential, handler and authorization are required")
	}
	origin := *u
	if origin.Scheme == "wss" {
		origin.Scheme = "https"
	} else {
		origin.Scheme = "http"
	}
	origin.Path = ""
	origin.RawQuery = ""
	return &Connector{config: cfg, endpoint: u.String(), origin: origin.String()}, nil
}

// Run retries outbound connections until cancellation. Revoking a grant must
// also cancel this context so an old credential cannot keep retrying.
func (c *Connector) Run(ctx context.Context) error {
	delay := time.Second
	for {
		started := time.Now()
		_ = c.Connect(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (c *Connector) Connect(ctx context.Context) error {
	cfg, err := websocket.NewConfig(c.endpoint, c.origin)
	if err != nil {
		return err
	}
	cfg.Header.Set("Authorization", "Bearer "+c.config.Credential)
	cfg.Header.Set(nativeSignInHeader, "1")
	cfg.Protocol = []string{protocolVersion}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	ws, err := cfg.DialContext(dialCtx)
	cancel()
	if err != nil {
		return errors.New("control-plane connection failed")
	}
	stopClose := context.AfterFunc(ctx, func() { _ = ws.Close() })
	defer stopClose()
	ws.MaxPayloadBytes = maxFrameSize
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello frame
	if err := websocket.JSON.Receive(ws, &hello); err != nil || hello.Type != "hello" || hello.Version != protocolVersion {
		_ = ws.Close()
		return errors.New("unsupported control-plane protocol")
	}
	s := newSession(ctx, ws)
	s.onRequest = func(f frame) { c.accept(s, f) }
	if c.config.OnConnected != nil {
		c.config.OnConnected()
	}
	if c.config.OnDisconnected != nil {
		defer c.config.OnDisconnected()
	}
	return s.run()
}

func (c *Connector) accept(s *session, f frame) {
	st, ok := s.add(f.ID)
	if !ok {
		s.close()
		return
	}
	go func() {
		defer s.remove(f.ID)
		w := &streamWriter{session: s, stream: st, id: f.ID, header: make(http.Header), op: f.Operation}
		defer func() {
			if recover() != nil {
				if !w.started {
					w.WriteHeader(500)
				}
			}
			w.finish()
		}()
		if f.ID == "" || (f.Operation != OperationUser && f.Operation != OperationWebhook && f.Operation != OperationWorkload && f.Operation != OperationNetwork && f.Operation != OperationNative) {
			http.Error(w, "invalid instance operation", 400)
			return
		}
		if f.Operation == OperationNative && !c.config.RemoteAccess {
			http.Error(w, "remote user access is disabled", 403)
			return
		}
		if f.Operation != OperationUser {
			f.Identity = Identity{}
		}
		if f.Operation == OperationUser && (!c.config.RemoteAccess || f.Identity.UserID == "") {
			http.Error(w, "remote user access is disabled", 403)
			return
		}
		if f.Operation == OperationWorkload && !validHost(f.Host) {
			http.Error(w, "invalid workload hostname", 400)
			return
		}
		u, err := url.ParseRequestURI(f.Path)
		if err != nil || !strings.HasPrefix(f.Path, "/") || strings.HasPrefix(f.Path, "//") || u.IsAbs() || u.Host != "" {
			http.Error(w, "invalid instance request path", 400)
			return
		}
		ctx := context.WithValue(st.ctx, operationKey{}, f.Operation)
		ctx = context.WithValue(ctx, identityKey{}, f.Identity)
		r, err := http.NewRequestWithContext(ctx, f.Method, f.Path, &streamBody{stream: st, session: s, id: f.ID})
		if err != nil {
			http.Error(w, "invalid instance request", 400)
			return
		}
		r.URL = u
		r.RequestURI = f.Path
		r.Header = cleanOperationHeaders(f.Header, f.Operation)
		r.Host = f.Host
		r.RemoteAddr = "instance-bridge"
		if f.Operation == OperationNative && !NativeRequestAllowed(r) {
			http.Error(w, "native request is not allowed", 403)
			return
		}
		if f.Operation == OperationNetwork && !NetworkRequestAllowed(r) {
			http.Error(w, "network operation is not allowed", 403)
			return
		}
		if err := c.config.Authorize(ctx, f.Operation, f.Identity, r); err != nil {
			http.Error(w, "instance access denied", 403)
			return
		}
		c.config.Handler.ServeHTTP(w, r)
	}()
}

type streamBody struct {
	stream  *stream
	session *session
	id      string
	pending []byte
	ended   bool
	total   int64
}

func (b *streamBody) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 {
		if b.ended {
			return 0, io.EOF
		}
		select {
		case <-b.stream.ctx.Done():
			return 0, b.stream.ctx.Err()
		case f := <-b.stream.frames:
			switch f.Type {
			case "request-data":
				b.total += int64(len(f.Data))
				if b.total > maxBodySize {
					return 0, errors.New("instance request body too large")
				}
				b.pending = f.Data
				if b.session.send(b.stream.ctx, frame{Type: "credit", ID: b.id}) != nil {
					return 0, errors.New("instance connection closed")
				}
			case "request-end":
				b.ended = true
			default:
				return 0, errors.New("unexpected instance request frame")
			}
		}
	}
	n := copy(dst, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}

func (b *streamBody) Close() error { b.ended = true; return nil }

type streamWriter struct {
	op      Operation
	session *session
	stream  *stream
	id      string
	header  http.Header
	started bool
	err     error
	mu      sync.Mutex
}

func (w *streamWriter) Header() http.Header { return w.header }

func (w *streamWriter) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeader(status)
}

func (w *streamWriter) writeHeader(status int) {
	if w.started {
		return
	}
	if status < 200 || status > 599 {
		status = 500
	}
	w.started = true
	w.err = w.session.send(w.stream.ctx, frame{Type: "response", ID: w.id, Status: status, Header: cleanOperationHeaders(w.header, w.op)})
}

func (w *streamWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeader(200)
	if w.err != nil {
		return 0, w.err
	}
	total := 0
	for len(data) > 0 {
		n := min(chunkSize, len(data))
		if err := w.session.sendData(w.stream, frame{Type: "data", ID: w.id, Data: append([]byte(nil), data[:n]...)}); err != nil {
			w.err = err
			return total, err
		}
		total += n
		data = data[n:]
	}
	return total, nil
}

func (w *streamWriter) Flush() { w.WriteHeader(200) }

func (w *streamWriter) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeader(200)
	if w.err != nil {
		w.session.notifyCancel(w.id)
		return
	}
	if err := w.session.send(w.stream.ctx, frame{Type: "end", ID: w.id}); err != nil {
		w.err = fmt.Errorf("finish instance response: %w", err)
	}
}
