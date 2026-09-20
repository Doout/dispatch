package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

func dashboardDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE history(kind VARCHAR,entity_id VARCHAR,project_id VARCHAR,name VARCHAR,state VARCHAR,started_at TIMESTAMP,finished_at TIMESTAMP,reused BOOLEAN,event_id BIGINT,PRIMARY KEY(kind,entity_id))`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func dashboardFact(t *testing.T, db *sql.DB, kind, id, project, name, state string, finish time.Time, seconds float64, reused bool) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO history VALUES(?,?,?,?,?,?,?,?,?)`, kind, id, project, name, state, finish.Add(-time.Duration(seconds*float64(time.Second))), finish, reused, 1)
	if err != nil {
		t.Fatal(err)
	}
}
func closeTo(t *testing.T, value *float64, want float64) {
	t.Helper()
	if value == nil || math.Abs(*value-want) > 0.000001 {
		t.Fatalf("got %v want %.3f", value, want)
	}
}
func estimated(t *testing.T, value *float64, want float64) {
	t.Helper()
	if value == nil || *value < want-0.000001 || *value > math.Max(want*1.05, durationResolution)+0.000001 {
		t.Fatalf("estimate %v outside histogram bound for %v", value, want)
	}
}
func TestDashboardAggregatesRetainedFactsAndEqualPeriods(t *testing.T) {
	db := dashboardDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	start := now.AddDate(0, 0, -7)
	for i, duration := range []float64{10, 20, 30, 40} {
		finish := now.Add(-time.Duration(i+1) * time.Hour)
		if i == 0 {
			finish = start
		}
		dashboardFact(t, db, "deployment", string(rune('a'+i)), "allowed", "checkout", "succeeded", finish, duration, false)
	}
	dashboardFact(t, db, "deployment", "failed-release", "allowed", "checkout", "failed", now.Add(-5*time.Hour), 4, false)
	dashboardFact(t, db, "deployment", "cancelled-release", "allowed", "checkout", "cancelled", now.Add(-6*time.Hour), 5, false)
	dashboardFact(t, db, "deployment", "previous-ok", "allowed", "checkout", "succeeded", start.AddDate(0, 0, -7), 60, false)
	dashboardFact(t, db, "deployment", "previous-failure", "allowed", "checkout", "failed", start.Add(-time.Microsecond), 7, false)
	dashboardFact(t, db, "job", "cached", "allowed", "build", "succeeded", now.Add(-time.Hour), 0, true)
	dashboardFact(t, db, "job", "built", "allowed", "build", "succeeded", now.Add(-time.Hour), 5, false)
	dashboardFact(t, db, "workflow", "flow", "allowed", "release", "succeeded", now.Add(-time.Hour), 90, false)
	dashboardFact(t, db, "deployment", "hidden-run", "hidden", "private workload", "failed", now.Add(-time.Hour), 5000, false)
	dashboardFact(t, db, "deployment", "hidden-old", "hidden", "private workload", "succeeded", now.AddDate(-1, 0, 0), 5000, false)
	dashboardFact(t, db, "deployment", "future", "allowed", "future", "succeeded", now, 12345, false)
	s := New(&memorySource{}, t.TempDir(), nil)
	if err := s.refreshAt(context.Background(), db, "ready", now); err != nil {
		t.Fatal(err)
	}
	out := s.Summary(map[string]bool{"allowed": true}, 7)
	if out.Totals.Deployments.Runs != 6 || out.Totals.Deployments.Succeeded != 4 || out.Totals.Deployments.Failed != 1 || out.Totals.Deployments.Cancelled != 1 {
		t.Fatalf("wrong outcomes %+v", out.Totals.Deployments)
	}
	if out.Totals.Deployments.DurationSamples != 4 || out.Totals.Deployments.DurationSeconds != 109 {
		t.Fatalf("wrong duration population %+v", out.Totals.Deployments)
	}
	closeTo(t, out.Totals.Deployments.SuccessRate, 80)
	closeTo(t, out.Totals.Deployments.MeanDurationSeconds, 25)
	estimated(t, out.Totals.Deployments.MedianDurationSeconds, 20)
	estimated(t, out.Totals.Deployments.P95DurationSeconds, 40)
	if out.PreviousTotals.Deployments.Runs != 2 {
		t.Fatal("previous boundary overlapped or lost facts", out.PreviousTotals)
	}
	closeTo(t, out.PreviousTotals.Deployments.SuccessRate, 50)
	closeTo(t, out.PreviousTotals.Deployments.MeanDurationSeconds, 60)
	if !out.Period.Start.Equal(start) || !out.Period.End.Equal(now) || !out.PreviousPeriod.End.Equal(start) || out.Period.End.Sub(out.Period.Start) != out.PreviousPeriod.End.Sub(out.PreviousPeriod.Start) {
		t.Fatal("periods are not adjacent equal windows")
	}
	if len(out.Daily) != 8 || out.Daily[0].Date != "2026-09-13" || out.Daily[7].Date != "2026-09-20" || out.Daily[0].Deployments.Runs != 1 || out.Daily[1].Deployments.Runs != 0 {
		t.Fatal("UTC buckets are not zero-filled with partial boundary days")
	}
	if out.Daily[1].Deployments.SuccessRate != nil || out.Daily[1].Deployments.MedianDurationSeconds != nil {
		t.Fatal("empty buckets invented a metric")
	}
	if out.Totals.Jobs.Reused != 1 || out.Totals.Jobs.DurationSamples != 1 {
		t.Fatal("job reuse distorted execution duration")
	}
	closeTo(t, out.Totals.Jobs.MeanDurationSeconds, 5)
	if len(out.FailureHotspots) != 1 || out.FailureHotspots[0].Name != "checkout" || out.FailureHotspots[0].LatestFailedDeploymentID != "failed-release" || out.FailureHotspots[0].FailureRate != 20 {
		t.Fatal("wrong failure hotspot", out.FailureHotspots)
	}
	if len(out.SlowWorkloads) != 3 || out.SlowWorkloads[0].Name != "release" || out.SlowWorkloads[0].LatestDeploymentID != "" {
		t.Fatal("wrong slow workload grouping", out.SlowWorkloads)
	}
	if !out.Coverage.FirstCompletedAt.Equal(start.AddDate(0, 0, -7)) || out.Coverage.LastCompletedAt.After(now) {
		t.Fatal("coverage escaped project/time boundary")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private workload") || strings.Contains(string(raw), "hidden-run") || strings.Contains(string(raw), "future") {
		t.Fatal("hidden or future facts leaked")
	}
	s.setState("unavailable")
	stale := s.Summary(map[string]bool{"allowed": true}, 7)
	if stale.State != "unavailable" || stale.Totals.Deployments.Runs != 6 || !stale.Period.End.Equal(now) || len(stale.FailureHotspots) != 1 {
		t.Fatal("worker failure lost the last immutable dashboard")
	}
	empty := s.Summary(map[string]bool{}, 7)
	if empty.Totals.Deployments.Runs != 0 || len(empty.FailureHotspots) != 0 || len(empty.SlowWorkloads) != 0 || empty.Coverage.FirstCompletedAt != nil {
		t.Fatal("empty grants exposed cached analytics")
	}
}

func TestDashboardPercentilesMergeSamplesAcrossProjects(t *testing.T) {
	db := dashboardDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	dashboardFact(t, db, "deployment", "one", "one", "first", "succeeded", now.Add(-time.Hour), 10, false)
	for i := 0; i < 99; i++ {
		dashboardFact(t, db, "deployment", string(rune(i+100)), "many", "second", "succeeded", now.Add(-time.Hour), 100, false)
	}
	s := New(&memorySource{}, t.TempDir(), nil)
	if err := s.refreshAt(context.Background(), db, "ready", now); err != nil {
		t.Fatal(err)
	}
	combined := s.Summary(map[string]bool{"one": true, "many": true}, 30)
	if combined.Totals.Deployments.DurationSamples != 100 {
		t.Fatal("sample count was not merged")
	}
	closeTo(t, combined.Totals.Deployments.MeanDurationSeconds, 99.1)
	estimated(t, combined.Totals.Deployments.MedianDurationSeconds, 100)
	estimated(t, combined.Totals.Deployments.P95DurationSeconds, 100)
	onlyOne := s.Summary(map[string]bool{"one": true}, 30)
	estimated(t, onlyOne.Totals.Deployments.MedianDurationSeconds, 10)
	if combined.Totals.Deployments.DurationSamples != 100 {
		t.Fatal("summary mutated shared histogram")
	}
}

func TestDashboardInvalidDurationsAndAllCancelledPeriod(t *testing.T) {
	db := dashboardDB(t)
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	dashboardFact(t, db, "deployment", "cancel", "allowed", "api", "cancelled", now.Add(-time.Hour), 10, false)
	dashboardFact(t, db, "job", "negative", "allowed", "bad-clock", "succeeded", now.Add(-time.Hour), -5, false)
	dashboardFact(t, db, "job", "zero", "allowed", "zero", "succeeded", now.Add(-time.Hour), 0, false)
	dashboardFact(t, db, "job", "small", "allowed", "small", "succeeded", now.Add(-time.Hour), .001, false)
	_, err := db.Exec(`INSERT INTO history VALUES('workflow','missing-start','allowed','missing','succeeded',NULL,?,false,1)`, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO history VALUES('workflow','zero-start','allowed','invalid-start','succeeded',TIMESTAMP '0001-01-01 00:00:00',?,false,1),('workflow','zero-finish','allowed','invalid-finish','succeeded',TIMESTAMP '0001-01-01 00:00:00',TIMESTAMP '0001-01-01 00:00:00',false,1)`, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s := New(&memorySource{}, t.TempDir(), nil)
	if err = s.refreshAt(context.Background(), db, "ready", now); err != nil {
		t.Fatal(err)
	}
	out := s.Summary(map[string]bool{"allowed": true}, 90)
	if out.Totals.Deployments.SuccessRate != nil || out.Totals.Deployments.MeanDurationSeconds != nil {
		t.Fatal("cancelled runs treated as failures or successful duration samples")
	}
	if out.Totals.Jobs.DurationSamples != 2 || out.Totals.Workflows.DurationSamples != 0 || out.Totals.Workflows.Runs != 2 {
		t.Fatal("invalid timestamps entered duration sample")
	}
	closeTo(t, out.Totals.Jobs.MedianDurationSeconds, 0)
	estimated(t, out.Totals.Jobs.P95DurationSeconds, .001)
	if out.Coverage.FirstCompletedAt == nil || !out.Coverage.FirstCompletedAt.Equal(now.Add(-time.Hour)) {
		t.Fatal("malformed timestamp sentinel polluted coverage")
	}
	if len(out.Daily) != 90 {
		t.Fatal("midnight cutoff included empty future day")
	}
}

func TestDashboardRankingsLimitAfterProjectScope(t *testing.T) {
	db := dashboardDB(t)
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	for _, project := range []string{"visible", "hidden"} {
		for kindIndex, kind := range []string{"deployment", "job", "workflow"} {
			for i := 0; i < 12; i++ {
				name := fmt.Sprintf("%s-%02d", kind, i)
				duration := float64(i + 1)
				if project == "hidden" {
					duration *= 1000
				}
				id := fmt.Sprintf("%s-%d-%d", project, kindIndex, i)
				dashboardFact(t, db, kind, id+"ok", project, name, "succeeded", now.Add(-time.Hour), duration, false)
				dashboardFact(t, db, kind, id+"fail", project, name, "failed", now.Add(-time.Hour), duration, false)
			}
		}
	}
	s := New(&memorySource{}, t.TempDir(), nil)
	if err := s.refreshAt(context.Background(), db, "ready", now); err != nil {
		t.Fatal(err)
	}
	out := s.Summary(map[string]bool{"visible": true}, 7)
	if len(out.FailureHotspots) != 30 || len(out.SlowWorkloads) != 30 {
		t.Fatal("rankings were truncated across kinds or before permission filtering")
	}
	for _, v := range out.FailureHotspots {
		if v.ProjectID != "visible" || (v.Kind != "deployment" && v.LatestFailedDeploymentID != "") {
			t.Fatal("hotspot leaked a project or invented a deployment ID")
		}
	}
	for _, v := range out.SlowWorkloads {
		if v.ProjectID != "visible" || v.MeanDurationSeconds > 12 {
			t.Fatal("slow ranking leaked hidden project")
		}
	}
	if out.SlowWorkloads[0].MeanDurationSeconds != 12 {
		t.Fatal("slow ranking did not use successful duration")
	}
}
