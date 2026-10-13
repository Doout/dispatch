package instanceconnection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/store"
)

func TestUsageReportIncludesPreviousDayWithoutOperationalData(t *testing.T) {
	m := testManager(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reports := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instance-connections/usage" || r.Header.Get("Authorization") != "Bearer private" {
			t.Error("invalid usage request")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		reports <- body
		w.WriteHeader(204)
	}))
	defer server.Close()
	m.config.Usage = func(_ context.Context, start, end time.Time) (store.TenantUsageTotals, error) {
		if end.Sub(start) != 24*time.Hour || start.Hour() != 0 {
			t.Error("invalid day boundary")
		}
		return store.TenantUsageTotals{Projects: 2, Applications: 3, Builds: 4, Deployments: 5, BuildSeconds: 6, Measured: []string{"projects", "applications", "builds", "deployments", "buildSeconds"}}, nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.reportUsage(ctx, saved{Status: Status{ControlPlaneURL: server.URL}, Credential: "private"})
	}()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, expected := range []time.Time{today.Add(-24 * time.Hour), today} {
		select {
		case body := <-reports:
			if body["periodStart"] != expected.Format(time.RFC3339) || body["projects"] != float64(2) || body["buildSeconds"] != float64(6) {
				t.Fatalf("unexpected aggregate report %+v", body)
			}
			if len(body) != 8 {
				t.Fatalf("unexpected data sent %+v", body)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("usage report missing")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("usage publisher did not stop")
	}
}
