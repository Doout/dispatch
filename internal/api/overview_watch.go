package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type changeSource interface{ Changes() <-chan struct{} }

// Watch uses the same authorization and filtering as GET /overview. Revalidate
// on every update and heartbeat so expired sessions cannot keep receiving data.
func (a *API) watchOverview(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Streaming unavailable", http.StatusNotImplemented)
		return
	}
	scope := overviewScope(r)
	version := r.Header.Get("Last-Event-ID")
	previous, ok := a.overviewSnapshots.find(scope, version)
	if !ok {
		http.Error(w, "Overview baseline expired", http.StatusConflict)
		return
	}
	var before any
	if err := json.Unmarshal(previous, &before); err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(event string, data []byte) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		err := controller.Flush()
		_ = controller.SetWriteDeadline(time.Time{})
		return err == nil
	}
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	// Flush headers immediately without sending an initial snapshot.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	if controller.Flush() != nil {
		return
	}
	for {
		// Subscribe before the read to avoid losing a write made during the snapshot.
		var changed <-chan struct{}
		if source, ok := a.store.(changeSource); ok {
			changed = source.Changes()
		}
		actor, valid := a.bearerIdentity(r)
		if !valid {
			send("auth-error", []byte(`401`))
			return
		}
		authResponse := &watchAuthResponse{header: make(http.Header)}
		ctx, valid := a.authorizedContext(authResponse, r, actor)
		if !valid {
			send("auth-error", []byte(fmt.Sprint(authResponse.status)))
			return
		}
		overview, err := a.overviewData(r.WithContext(ctx))
		if err != nil {
			send("error", []byte(`"Overview unavailable"`))
			return
		}
		data, err := json.Marshal(overview)
		if err != nil {
			return
		}
		var after any
		if err := json.Unmarshal(data, &after); err != nil {
			return
		}
		var ops []overviewPatch
		diffOverview(before, after, "", &ops)
		if len(ops) > 0 {
			nextVersion := a.overviewSnapshots.remember(scope, data)
			patch, err := json.Marshal(struct {
				Base    string          `json:"base"`
				Version string          `json:"version"`
				Ops     []overviewPatch `json:"ops"`
			}{version, nextVersion, ops})
			if err != nil || !send("patch", patch) {
				return
			}
			before, version = after, nextVersion
		}
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_, err := fmt.Fprint(w, ": keepalive\n\n")
			if err != nil || controller.Flush() != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Time{})
		case <-changed:
			// Batch bursts of job/log writes into at most two snapshots per second.
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

// Authorization failures are sent as stream events after headers are flushed.
type watchAuthResponse struct {
	header http.Header
	status int
}

func (w *watchAuthResponse) Header() http.Header         { return w.header }
func (w *watchAuthResponse) WriteHeader(status int)      { w.status = status }
func (w *watchAuthResponse) Write(p []byte) (int, error) { return len(p), nil }
