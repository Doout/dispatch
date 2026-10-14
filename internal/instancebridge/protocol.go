// Package instancebridge carries HTTP operations over a connection initiated by
// a self-hosted installation. It does not open connections to customer servers.
package instancebridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

type Operation string

const (
	OperationUser     Operation = "user"
	OperationWebhook  Operation = "webhook"
	OperationWorkload Operation = "workload"
	OperationNetwork  Operation = "network"
	OperationNative   Operation = "native-user"
	protocolVersion             = "doodship-instance-v1"
	chunkSize                   = 16 << 10
	maxFrameSize                = 64 << 10
	maxStreams                  = 64
	maxBodySize                 = 64 << 20
)

type Identity struct {
	UserID string `json:"userId,omitempty"`
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role,omitempty"`
}

type identityKey struct{}
type operationKey struct{}

func IdentityFromContext(ctx context.Context) (Identity, bool) {
	i, ok := ctx.Value(identityKey{}).(Identity)
	return i, ok && i.UserID != ""
}

func OperationFromContext(ctx context.Context) Operation {
	op, _ := ctx.Value(operationKey{}).(Operation)
	return op
}

type frame struct {
	Type      string      `json:"type"`
	Version   string      `json:"version,omitempty"`
	ID        string      `json:"id,omitempty"`
	Operation Operation   `json:"operation,omitempty"`
	Identity  Identity    `json:"identity,omitempty"`
	Method    string      `json:"method,omitempty"`
	Path      string      `json:"path,omitempty"`
	Host      string      `json:"host,omitempty"`
	Header    http.Header `json:"header,omitempty"`
	Status    int         `json:"status,omitempty"`
	Data      []byte      `json:"data,omitempty"`
}

type stream struct {
	frames  chan frame
	credits chan struct{}
	ctx     context.Context
	cancel  context.CancelFunc
}

type session struct {
	nativeSignIn bool
	ws           *websocket.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	streams      map[string]*stream
	out          chan frame
	onRequest    func(frame)
}

func newSession(ctx context.Context, ws *websocket.Conn) *session {
	ctx, cancel := context.WithCancel(ctx)
	ws.MaxPayloadBytes = maxFrameSize
	return &session{ws: ws, ctx: ctx, cancel: cancel, streams: make(map[string]*stream), out: make(chan frame, 32)}
}

func (s *session) close() {
	s.cancel()
	_ = s.ws.Close()
	s.mu.Lock()
	for _, st := range s.streams {
		st.cancel()
	}
	s.mu.Unlock()
}

func (s *session) add(id string) (*stream, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || len(s.streams) >= maxStreams || s.streams[id] != nil {
		return nil, false
	}
	ctx, cancel := context.WithCancel(s.ctx)
	st := &stream{frames: make(chan frame, 32), ctx: ctx, cancel: cancel}
	st.credits = make(chan struct{}, 16)
	for range 16 {
		st.credits <- struct{}{}
	}
	s.streams[id] = st
	return st, true
}

func (s *session) remove(id string) {
	s.mu.Lock()
	if st := s.streams[id]; st != nil {
		st.cancel()
		delete(s.streams, id)
	}
	s.mu.Unlock()
}

func (s *session) notifyCancel(id string) {
	select {
	case s.out <- frame{Type: "cancel", ID: id}:
	default:
		s.close()
	}
}

func (s *session) send(ctx context.Context, f frame) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if f.Type == "request" || f.Type == "response" {
		encoded, err := json.Marshal(f)
		if err != nil || len(encoded) > maxFrameSize {
			return errors.New("instance request metadata too large")
		}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errors.New("instance connection closed")
	case <-timer.C:
		return errors.New("instance connection is busy")
	case s.out <- f:
		return nil
	}
}

func (s *session) sendData(st *stream, f frame) error {
	select {
	case <-st.ctx.Done():
		return st.ctx.Err()
	case <-st.credits:
	}
	return s.send(st.ctx, f)
}

func (s *session) run() error {
	defer s.close()
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			var f frame
			select {
			case <-s.ctx.Done():
				return
			case f = <-s.out:
			case <-ticker.C:
				f = frame{Type: "ping"}
			}
			_ = s.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if websocket.JSON.Send(s.ws, f) != nil {
				s.close()
				return
			}
		}
	}()
	go func() { <-s.ctx.Done(); _ = s.ws.Close() }()
	for {
		_ = s.ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		var f frame
		if err := websocket.JSON.Receive(s.ws, &f); err != nil {
			return err
		}
		if len(f.Data) > chunkSize {
			return errors.New("instance frame too large")
		}
		switch f.Type {
		case "ping":
			if err := s.send(s.ctx, frame{Type: "pong"}); err != nil {
				return err
			}
			continue
		case "pong":
			continue
		case "request":
			if s.onRequest == nil {
				return errors.New("unexpected instance request")
			}
			s.onRequest(f)
			continue
		case "cancel":
			s.remove(f.ID)
			continue
		case "credit":
			overflow := false
			s.mu.Lock()
			if st := s.streams[f.ID]; st != nil {
				select {
				case st.credits <- struct{}{}:
				default:
					st.cancel()
					delete(s.streams, f.ID)
					overflow = true
				}
			}
			s.mu.Unlock()
			if overflow {
				s.notifyCancel(f.ID)
			}
			continue
		case "request-data", "request-end", "response", "data", "end":
		default:
			return errors.New("unknown instance frame")
		}
		s.mu.Lock()
		overflow := false
		st := s.streams[f.ID]
		if st != nil {
			select {
			case st.frames <- f:
			default:
				st.cancel()
				delete(s.streams, f.ID)
				overflow = true
			}
		}
		s.mu.Unlock()
		if overflow {
			s.notifyCancel(f.ID)
		}
	}
}

// cleanHeaders prevents a browser from injecting local credentials or proxy
// identity. Identity is carried separately and exposed only through context.
func cleanHeaders(src http.Header) http.Header {
	return cleanOperationHeaders(src, OperationUser)
}

func cleanOperationHeaders(src http.Header, op Operation) http.Header {
	h := src.Clone()
	for _, name := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(name))
	}
	for name := range h {
		lower := strings.ToLower(name)
		if op != OperationWorkload && lower == "impersonate-user" {
			delete(h, name)
			continue
		}
		if (op != OperationWorkload && op != OperationNetwork && lower == "authorization") || (op != OperationWorkload && op != OperationNative && (lower == "cookie" || lower == "set-cookie")) || lower == "connection" || lower == "upgrade" || lower == "proxy-authorization" || lower == "proxy-authenticate" || lower == "keep-alive" || lower == "transfer-encoding" || lower == "trailer" || lower == "te" || lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || strings.HasPrefix(lower, "x-dispatch-") || strings.HasPrefix(lower, "x-doodship-") || strings.HasPrefix(lower, "x-remote-") {
			delete(h, name)
		}
	}
	if op == OperationNative {
		filterNativeCookies(h)
	}
	if op == OperationWorkload {
		cookieReq := &http.Request{Header: h}
		cookies := cookieReq.Cookies()
		h.Del("Cookie")
		for _, cookie := range cookies {
			if !controlCookie(cookie.Name) {
				cookieReq.AddCookie(cookie)
			}
		}
		values := h.Values("Set-Cookie")
		h.Del("Set-Cookie")
		for _, value := range values {
			name, _, ok := strings.Cut(value, "=")
			if ok && !controlCookie(strings.TrimSpace(name)) {
				h.Add("Set-Cookie", value)
			}
		}
	}
	return h
}

func controlCookie(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "__host-dispatch-") || strings.HasPrefix(name, "__host-doodship-") || strings.HasPrefix(name, "dispatch_") || strings.HasPrefix(name, "doodship_")
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == ':') {
			return false
		}
	}
	return true
}
