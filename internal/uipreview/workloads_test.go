package uipreview

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkloadHostsBypassConsoleAssetsAndProductionDataSelector(t *testing.T) {
	calls := 0
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if !previewWorkloadPrefix.MatchString(r.Host) && !strings.HasPrefix(r.Host, "pr-223-team-") && !strings.HasPrefix(r.Host, "app-") {
			t.Error("registered workload host was lost")
		}
		if strings.Contains(r.Header.Get("Cookie"), "dispatch") || strings.Contains(r.Header.Get("Cookie"), "production") {
			t.Error("console credentials reached workload")
		}
		if r.Header.Get("Cookie") != "app=session" {
			t.Error("application cookie was lost")
		}
		w.WriteHeader(404)
	}))
	defer dev.Close()
	s, err := New(Config{Origin: "https://dev.example.test", Development: dev.URL, Production: "https://prod.example.test", AccessKey: testAccessKey})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"p223-abc-team", "app-abc-team", "pr-223-team-abc", "app-team-abc"} {
		for _, path := range []string{"/", "/assets/application.js", "/api/v1/hosted", sourcePath} {
			r := httptest.NewRequest("GET", "https://"+slug+".dev.example.test"+path, nil)
			for _, name := range []string{sourceCookie, accessCookie, "__Host-dispatch-tenant", productionCookiePrefix + "anything"} {
				r.AddCookie(&http.Cookie{Name: name, Value: "production"})
			}
			r.AddCookie(&http.Cookie{Name: "app", Value: "session"})
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatal("workload got a console page instead of the backend status", w.Code)
			}
		}
	}
	if calls != 16 {
		t.Fatal("workload did not reach its dev backend", calls)
	}
}
