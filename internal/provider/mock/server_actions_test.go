package mock_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
)

func TestServerActionConformanceAndFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		options mock.Options
		success bool
	}{{"complete", mock.Options{}, true}, {"power-failed", mock.Options{FailPower: true}, false}, {"promotion-failed", mock.Options{FailPromotion: true}, false}} {
		t.Run(test.name, func(t *testing.T) {
			test.options.Polls = 1
			a, e := mock.New(test.options)
			if e != nil {
				t.Fatal(e)
			}
			s := httptest.NewServer(provider.Handler(a, ""))
			defer s.Close()
			client, _ := provider.NewClient(s.URL, "", s.Client())
			report, e := provider.RunServerActionConformance(context.Background(), client, provider.ConformanceOptions{Request: request(), PollInterval: time.Millisecond, Timeout: 2 * time.Second})
			if (e == nil) != test.success {
				t.Fatal(report, e)
			}
			last := report.Checks[len(report.Checks)-1]
			if last.Name != "machine action cleanup" || !last.Passed {
				t.Fatal("resource cleanup absent", report)
			}
		})
	}
}
func TestMachinePowerPersistsAcrossAdapterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "adapter.json")
	a, e := mock.New(mock.Options{StateFile: path, Polls: 1})
	if e != nil {
		t.Fatal(e)
	}
	created, e := a.CreateServer(ctx, "source", request())
	if e != nil {
		t.Fatal(e)
	}
	a.Operation(ctx, created.ID)
	s, _ := a.Server(ctx, created.ResourceID)
	in := provider.PowerServerRequest{Action: "stop", ExpectedIdentity: provider.ServerIdentityDigest(s)}
	first, e := a.PowerServer(ctx, "stop-once", s.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	a, e = mock.New(mock.Options{StateFile: path, Polls: 1})
	if e != nil {
		t.Fatal(e)
	}
	again, e := a.PowerServer(ctx, "stop-once", s.ID, in)
	if e != nil || first != again {
		t.Fatal("restart changed operation", again, e)
	}
	op, e := a.Operation(ctx, again.ID)
	if e != nil || op.State != provider.StateSucceeded {
		t.Fatal(op, e)
	}
	s, e = a.Server(ctx, s.ID)
	if e != nil || provider.VerifyPowerEvidence(s, op.ID, in) != nil {
		t.Fatal("power not persisted", s, e)
	}
}
