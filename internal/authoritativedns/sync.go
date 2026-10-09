package authoritativedns

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SnapshotHandler exposes a read-only replication feed. Mount it separately
// from browser APIs. Replication uses a dedicated secret with no tenant access.
func SnapshotHandler(token string, source func(context.Context) (Snapshot, error)) http.Handler {
	expected := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		actual := sha256.Sum256([]byte(provided))
		if len(token) < 32 || !ok || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := source(r.Context())
		if err != nil {
			http.Error(w, "DNS snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		z, err := compile(snapshot)
		if err != nil {
			http.Error(w, "DNS snapshot invalid", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(z.encoded)
	})
}

type Poller struct {
	URL      string
	Token    string
	Server   *Server
	Interval time.Duration
	Client   *http.Client
	OnError  func(error)
}

func (p Poller) Sync(ctx context.Context) error {
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("DNS snapshot URL must use HTTPS")
	}
	if len(p.Token) < 32 || p.Server == nil {
		return errors.New("DNS sync requires a server and a dedicated token of at least 32 characters")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+p.Token)
	client := http.Client{Timeout: 30 * time.Second}
	if p.Client != nil {
		client = *p.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("DNS snapshot request failed")
	}
	const max = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil {
		return err
	}
	if len(raw) > max {
		return errors.New("DNS snapshot is too large")
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&snapshot); err != nil {
		return err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid trailing DNS snapshot data")
	}
	return p.Server.Apply(snapshot)
}

func (p Poller) Run(ctx context.Context) {
	interval := p.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	for {
		if err := p.Sync(ctx); err != nil && ctx.Err() == nil && p.OnError != nil {
			p.OnError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
