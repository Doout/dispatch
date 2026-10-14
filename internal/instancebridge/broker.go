package instancebridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"
)

type Broker struct {
	mu       sync.RWMutex
	sessions map[string]*session
	requests chan struct{}
}

func NewBroker() *Broker {
	return &Broker{sessions: make(map[string]*session), requests: make(chan struct{}, 128)}
}

// ServeConnection must be called only after validating the installation's
// bearer credential and current connection grant. key must come from that grant.
func (b *Broker) ServeConnection(w http.ResponseWriter, r *http.Request, key string) {
	if r.Context().Err() != nil {
		http.Error(w, "instance connection canceled", 503)
		return
	}
	if key == "" {
		http.Error(w, "invalid instance connection", http.StatusUnauthorized)
		return
	}
	websocket.Server{
		Handshake: func(cfg *websocket.Config, r *http.Request) error {
			if len(cfg.Protocol) != 1 || cfg.Protocol[0] != protocolVersion {
				return errors.New("unsupported instance protocol")
			}
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			s := newSession(r.Context(), ws)
			s.nativeSignIn = r.Header.Get(nativeSignInHeader) == "1"
			stopClose := context.AfterFunc(s.ctx, func() { _ = ws.Close() })
			defer stopClose()
			b.mu.Lock()
			if s.ctx.Err() != nil {
				b.mu.Unlock()
				s.close()
				return
			}
			if len(b.sessions) >= 8192 && b.sessions[key] == nil {
				b.mu.Unlock()
				s.close()
				return
			}
			old := b.sessions[key]
			b.sessions[key] = s
			b.mu.Unlock()
			if old != nil {
				old.close()
			}
			defer func() {
				b.mu.Lock()
				if b.sessions[key] == s {
					delete(b.sessions, key)
				}
				b.mu.Unlock()
			}()
			_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if websocket.JSON.Send(ws, frame{Type: "hello", Version: protocolVersion}) != nil {
				s.close()
				return
			}
			_ = s.run()
		},
	}.ServeHTTP(w, r)
}

func (b *Broker) Connected(key string) bool {
	b.mu.RLock()
	s := b.sessions[key]
	b.mu.RUnlock()
	return s != nil && s.ctx.Err() == nil
}

func (b *Broker) NativeSignInAvailable(key string) bool {
	b.mu.RLock()
	s := b.sessions[key]
	b.mu.RUnlock()
	return s != nil && s.nativeSignIn && s.ctx.Err() == nil
}

func (b *Broker) Disconnect(key string) {
	b.mu.Lock()
	s := b.sessions[key]
	delete(b.sessions, key)
	b.mu.Unlock()
	if s != nil {
		s.close()
	}
}

func (b *Broker) Close() {
	b.mu.Lock()
	sessions := b.sessions
	b.sessions = make(map[string]*session)
	b.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}

func (b *Broker) Proxy(w http.ResponseWriter, r *http.Request, key string, identity Identity) {
	b.ProxyOperation(w, r, key, OperationUser, identity)
}

func (b *Broker) ProxyOperation(w http.ResponseWriter, r *http.Request, key string, op Operation, identity Identity) {
	if op != OperationUser && op != OperationWebhook && op != OperationWorkload && op != OperationNetwork && op != OperationNative {
		http.Error(w, "invalid instance operation", 400)
		return
	}
	if op == OperationNative && !NativeRequestAllowed(r) {
		http.Error(w, "native request is not allowed", 403)
		return
	}
	if op == OperationUser && identity.UserID == "" {
		http.Error(w, "instance identity required", 401)
		return
	}
	if op == OperationNetwork && !NetworkRequestAllowed(r) {
		http.Error(w, "network operation is not allowed", 403)
		return
	}
	if op != OperationUser {
		identity = Identity{}
	}
	if r.Header.Get("Upgrade") != "" {
		http.Error(w, "protocol upgrade is not supported", 400)
		return
	}
	if op == OperationWorkload && !validHost(r.Host) {
		http.Error(w, "invalid workload hostname", 400)
		return
	}
	select {
	case b.requests <- struct{}{}:
		defer func() { <-b.requests }()
	default:
		http.Error(w, "instance gateway is busy", 503)
		return
	}
	b.mu.RLock()
	s := b.sessions[key]
	b.mu.RUnlock()
	if s == nil {
		http.Error(w, "instance is offline", 503)
		return
	}
	if op == OperationNative && !s.nativeSignIn {
		http.Error(w, "update the installation to enable instance sign-in", 503)
		return
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		http.Error(w, "unable to open instance request", 500)
		return
	}
	id := hex.EncodeToString(nonce[:])
	st, ok := s.add(id)
	if !ok {
		http.Error(w, "instance connection is busy", 503)
		return
	}
	defer s.remove(id)
	ctx, cancel := context.WithCancel(st.ctx)
	defer cancel()
	stop := context.AfterFunc(r.Context(), cancel)
	defer stop()
	controller := http.NewResponseController(w)
	writeStopped := make(chan struct{})
	stopWrite := context.AfterFunc(ctx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		_ = controller.SetReadDeadline(time.Now())
		close(writeStopped)
	})
	var stopped sync.Once
	stopDeadlines := func() {
		stopped.Do(func() {
			if !stopWrite() {
				<-writeStopped
			}
		})
	}
	defer func() {
		stopDeadlines()
		_ = controller.SetWriteDeadline(time.Time{})
		_ = controller.SetReadDeadline(time.Time{})
	}()
	if err := s.send(ctx, frame{Type: "request", ID: id, Operation: op, Identity: identity, Method: r.Method, Path: r.URL.RequestURI(), Host: r.Host, Header: cleanOperationHeaders(r.Header, op)}); err != nil {
		http.Error(w, "instance is offline", 503)
		return
	}
	defer func() { _ = s.send(s.ctx, frame{Type: "cancel", ID: id}) }()
	var bodyFinished atomic.Bool
	bodyFinished.Store(r.Body == nil || r.Body == http.NoBody)
	if r.Body != nil {
		defer func() {
			stopDeadlines()
			cancel()
			if !bodyFinished.Load() {
				_ = controller.SetReadDeadline(time.Now())
			}
			_ = r.Body.Close()
		}()
		context.AfterFunc(ctx, func() { _ = r.Body.Close() })
	}
	go func() {
		buf := make([]byte, chunkSize)
		var total int64
		if r.Body != nil {
			for {
				n, err := r.Body.Read(buf)
				if n > 0 {
					total += int64(n)
					if total > maxBodySize {
						cancel()
						return
					}
					if s.sendData(st, frame{Type: "request-data", ID: id, Data: append([]byte(nil), buf[:n]...)}) != nil {
						cancel()
						return
					}
				}
				if err == io.EOF {
					bodyFinished.Store(true)
					break
				}
				if err != nil {
					cancel()
					return
				}
			}
		}
		if s.send(ctx, frame{Type: "request-end", ID: id}) != nil {
			cancel()
		}
	}()
	started := false
	for {
		select {
		case <-ctx.Done():
			if !started {
				http.Error(w, "instance request interrupted", 502)
			}
			return
		case f := <-st.frames:
			switch f.Type {
			case "response":
				if started || f.Status < 100 || f.Status > 599 {
					return
				}
				for name, values := range cleanOperationHeaders(f.Header, op) {
					if strings.EqualFold(name, "Set-Cookie") {
						for _, value := range values {
							w.Header().Add(name, value)
						}
					} else {
						w.Header()[name] = values
					}
				}
				w.WriteHeader(f.Status)
				started = true
				_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if flush, ok := w.(http.Flusher); ok {
					flush.Flush()
				}
			case "data":
				if !started {
					return
				}
				_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if _, err := w.Write(f.Data); err != nil {
					return
				}
				if flush, ok := w.(http.Flusher); ok {
					flush.Flush()
				}
				if s.send(ctx, frame{Type: "credit", ID: id}) != nil {
					return
				}
			case "end":
				return
			default:
				return
			}
		}
	}
}
