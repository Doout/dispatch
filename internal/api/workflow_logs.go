package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

type workflowLogDelta struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error"`
	Keep  int    `json:"keep"`
	Text  string `json:"text"`
}

func workflowLogChange(before, after core.WorkflowJobResult) workflowLogDelta {
	oldText, newText := strings.ToValidUTF8(before.Log, "�"), strings.ToValidUTF8(after.Log, "�")
	n := 0
	for n < len(oldText) && n < len(newText) && oldText[n] == newText[n] {
		n++
	}
	for n > 0 && n < len(newText) && !utf8.RuneStart(newText[n]) {
		n--
	}
	return workflowLogDelta{after.ID, after.JobName, after.State, after.Error, len(utf16.Encode([]rune(newText[:n]))), newText[n:]}
}

// Only an open log viewer reads this stream. Each job sends its initial log once,
// followed by changed suffixes and state changes, without repository metadata.
func (a *API) watchWorkflowLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Streaming unavailable", 501)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(event string, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		err = controller.Flush()
		_ = controller.SetWriteDeadline(time.Time{})
		return err == nil
	}
	if !send("connected", nil) {
		return
	}
	previous := map[string]core.WorkflowJobResult{}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		actor, valid := a.bearerIdentity(r)
		if !valid {
			send("auth-error", 401)
			return
		}
		auth := &watchAuthResponse{header: make(http.Header)}
		ctx, valid := a.authorizedContext(auth, r, actor)
		if !valid {
			send("auth-error", 403)
			return
		}
		allowed := false
		a.workflowRevisionPermission(core.PermissionProjectView, func(http.ResponseWriter, *http.Request) { allowed = true })(auth, r.WithContext(ctx))
		if !allowed {
			send("auth-error", 403)
			return
		}
		revision, err := a.store.GetWorkflowRevision(ctx, chi.URLParam(r, "id"))
		if err != nil {
			send("error", "Build run unavailable")
			return
		}
		jobs, err := a.store.ListWorkflowJobResults(ctx, revision.ID)
		if err != nil {
			send("error", "Build logs unavailable")
			return
		}
		for _, job := range jobs {
			before, exists := previous[job.ID]
			if !exists || before.Log != job.Log || before.State != job.State || before.Error != job.Error {
				if !send("log", workflowLogChange(before, job)) {
					return
				}
				previous[job.ID] = job
			}
		}
		if revision.State != "queued" && revision.State != "running" {
			send("complete", revision.State)
			return
		}
		ticks++
		if ticks%30 == 0 && !send("heartbeat", nil) {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
