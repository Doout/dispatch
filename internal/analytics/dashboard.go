package analytics

import (
	"context"
	"database/sql"
	"math"
	"sort"
	"strings"
	"time"
)

// Duration percentiles use mergeable logarithmic histograms. The upper bound is
// within 5% of a positive sample, or 10ms for sub-resolution samples. Histograms
// merge after authorization; averaging project percentiles would be incorrect.
const durationResolution = 0.01
const durationBucketRatio = 1.05

type Counts struct {
	Runs                  int64    `json:"runs"`
	Succeeded             int64    `json:"succeeded"`
	Failed                int64    `json:"failed"`
	Cancelled             int64    `json:"cancelled"`
	Reused                int64    `json:"reused"`
	DurationSeconds       float64  `json:"durationSeconds"`
	DurationSamples       int64    `json:"durationSamples"`
	SuccessRate           *float64 `json:"successRate"`
	MeanDurationSeconds   *float64 `json:"meanDurationSeconds"`
	MedianDurationSeconds *float64 `json:"medianDurationSeconds"`
	P95DurationSeconds    *float64 `json:"p95DurationSeconds"`
	sampleSeconds         float64
	histogram             map[int]int64
}
type Day struct {
	Date        string `json:"date"`
	Deployments Counts `json:"deployments"`
	Workflows   Counts `json:"workflows"`
	Jobs        Counts `json:"jobs"`
}
type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type FailureHotspot struct {
	ProjectID                string     `json:"projectId"`
	Kind                     string     `json:"kind"`
	Name                     string     `json:"name"`
	Runs                     int64      `json:"runs"`
	Succeeded                int64      `json:"succeeded"`
	Failed                   int64      `json:"failed"`
	Cancelled                int64      `json:"cancelled"`
	FailureRate              float64    `json:"failureRate"`
	LatestFailedAt           *time.Time `json:"latestFailedAt,omitempty"`
	LatestFailedDeploymentID string     `json:"latestFailedDeploymentId,omitempty"`
	days                     int
}
type SlowWorkload struct {
	ProjectID           string  `json:"projectId"`
	Kind                string  `json:"kind"`
	Name                string  `json:"name"`
	Runs                int64   `json:"runs"`
	DurationSamples     int64   `json:"durationSamples"`
	MeanDurationSeconds float64 `json:"meanDurationSeconds"`
	LatestDeploymentID  string  `json:"latestDeploymentId,omitempty"`
	days                int
}
type Capabilities struct {
	DurationPercentiles                 string  `json:"durationPercentiles"`
	DurationPercentileMaxErrorPercent   int     `json:"durationPercentileMaxErrorPercent"`
	DurationPercentileResolutionSeconds float64 `json:"durationPercentileResolutionSeconds"`
	DurationPopulation                  string  `json:"durationPopulation"`
	StageTiming                         bool    `json:"stageTiming"`
	EnvironmentBreakdown                bool    `json:"environmentBreakdown"`
}
type Coverage struct {
	FirstCompletedAt *time.Time `json:"firstCompletedAt,omitempty"`
	LastCompletedAt  *time.Time `json:"lastCompletedAt,omitempty"`
}
type Summary struct {
	State           string           `json:"state"`
	UpdatedAt       *time.Time       `json:"updatedAt,omitempty"`
	Days            int              `json:"days"`
	Daily           []Day            `json:"daily"`
	Totals          Day              `json:"totals"`
	Period          Period           `json:"period"`
	PreviousPeriod  Period           `json:"previousPeriod"`
	PreviousTotals  Day              `json:"previousTotals"`
	FailureHotspots []FailureHotspot `json:"failureHotspots"`
	SlowWorkloads   []SlowWorkload   `json:"slowWorkloads"`
	Capabilities    Capabilities     `json:"capabilities"`
	Coverage        Coverage         `json:"coverage"`
}
type row struct {
	Project, Date, Kind string
	Days                int
	Previous            bool
	Counts              Counts
}
type projectCoverage struct {
	Project  string
	Coverage Coverage
}
type snapshot struct {
	State     string
	UpdatedAt *time.Time
	Rows      []row
	Failures  []FailureHotspot
	Slow      []SlowWorkload
	Coverage  []projectCoverage
}

func normalizeDays(days int) int {
	if days != 7 && days != 30 && days != 90 {
		return 30
	}
	return days
}
func EmptySummary(state string, days int) Summary {
	return summarize(&snapshot{State: state}, nil, days, time.Now().UTC())
}
func (s *Service) Summary(projects map[string]bool, days int) Summary {
	current := s.current.Load()
	now := time.Now().UTC()
	if current.UpdatedAt != nil {
		now = *current.UpdatedAt
	}
	return summarize(current, projects, days, now)
}
func summarize(current *snapshot, projects map[string]bool, days int, now time.Time) Summary {
	days = normalizeDays(days)
	start := now.AddDate(0, 0, -days)
	result := Summary{State: current.State, UpdatedAt: current.UpdatedAt, Days: days, Daily: []Day{}, Period: Period{Start: start, End: now}, PreviousPeriod: Period{Start: start.AddDate(0, 0, -days), End: start}, FailureHotspots: []FailureHotspot{}, SlowWorkloads: []SlowWorkload{}, Capabilities: Capabilities{DurationPercentiles: "histogram", DurationPercentileMaxErrorPercent: 5, DurationPercentileResolutionSeconds: durationResolution, DurationPopulation: "successful_non_reused"}}
	indices := map[string]int{}
	for day := start.Truncate(24 * time.Hour); day.Before(now); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		indices[date] = len(result.Daily)
		result.Daily = append(result.Daily, Day{Date: date})
	}
	for _, r := range current.Rows {
		if r.Days != days || !projects[r.Project] {
			continue
		}
		if r.Previous {
			add(counts(&result.PreviousTotals, r.Kind), r.Counts)
			continue
		}
		add(counts(&result.Totals, r.Kind), r.Counts)
		if i, ok := indices[r.Date]; ok {
			add(counts(&result.Daily[i], r.Kind), r.Counts)
		}
	}
	finalizeDay(&result.Totals)
	finalizeDay(&result.PreviousTotals)
	for i := range result.Daily {
		finalizeDay(&result.Daily[i])
	}
	for _, f := range current.Failures {
		if f.days == days && projects[f.ProjectID] {
			result.FailureHotspots = append(result.FailureHotspots, f)
		}
	}
	sort.Slice(result.FailureHotspots, func(i, j int) bool {
		a, b := result.FailureHotspots[i], result.FailureHotspots[j]
		if a.Failed != b.Failed {
			return a.Failed > b.Failed
		}
		if a.FailureRate != b.FailureRate {
			return a.FailureRate > b.FailureRate
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ProjectID < b.ProjectID
	})
	result.FailureHotspots = limitFailures(result.FailureHotspots)
	for _, v := range current.Slow {
		if v.days == days && projects[v.ProjectID] {
			result.SlowWorkloads = append(result.SlowWorkloads, v)
		}
	}
	sort.Slice(result.SlowWorkloads, func(i, j int) bool {
		a, b := result.SlowWorkloads[i], result.SlowWorkloads[j]
		if a.MeanDurationSeconds != b.MeanDurationSeconds {
			return a.MeanDurationSeconds > b.MeanDurationSeconds
		}
		if a.DurationSamples != b.DurationSamples {
			return a.DurationSamples > b.DurationSamples
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ProjectID < b.ProjectID
	})
	result.SlowWorkloads = limitSlow(result.SlowWorkloads)
	for _, v := range current.Coverage {
		if !projects[v.Project] {
			continue
		}
		if v.Coverage.FirstCompletedAt != nil && (result.Coverage.FirstCompletedAt == nil || v.Coverage.FirstCompletedAt.Before(*result.Coverage.FirstCompletedAt)) {
			result.Coverage.FirstCompletedAt = v.Coverage.FirstCompletedAt
		}
		if v.Coverage.LastCompletedAt != nil && (result.Coverage.LastCompletedAt == nil || v.Coverage.LastCompletedAt.After(*result.Coverage.LastCompletedAt)) {
			result.Coverage.LastCompletedAt = v.Coverage.LastCompletedAt
		}
	}
	return result
}
func limitFailures(items []FailureHotspot) []FailureHotspot {
	seen := map[string]int{}
	result := make([]FailureHotspot, 0, len(items))
	for _, v := range items {
		if seen[v.Kind] < 10 {
			result = append(result, v)
			seen[v.Kind]++
		}
	}
	return result
}
func limitSlow(items []SlowWorkload) []SlowWorkload {
	seen := map[string]int{}
	result := make([]SlowWorkload, 0, len(items))
	for _, v := range items {
		if seen[v.Kind] < 10 {
			result = append(result, v)
			seen[v.Kind]++
		}
	}
	return result
}
func counts(day *Day, kind string) *Counts {
	switch kind {
	case "deployment":
		return &day.Deployments
	case "workflow":
		return &day.Workflows
	default:
		return &day.Jobs
	}
}
func add(a *Counts, b Counts) {
	a.Runs += b.Runs
	a.Succeeded += b.Succeeded
	a.Failed += b.Failed
	a.Cancelled += b.Cancelled
	a.Reused += b.Reused
	a.DurationSeconds += b.DurationSeconds
	a.DurationSamples += b.DurationSamples
	a.sampleSeconds += b.sampleSeconds
	if len(b.histogram) > 0 && a.histogram == nil {
		a.histogram = map[int]int64{}
	}
	for bucket, n := range b.histogram {
		a.histogram[bucket] += n
	}
}
func finalizeDay(day *Day) { finalize(&day.Deployments); finalize(&day.Workflows); finalize(&day.Jobs) }
func finalize(c *Counts) {
	if n := c.Succeeded + c.Failed; n > 0 {
		v := 100 * float64(c.Succeeded) / float64(n)
		c.SuccessRate = &v
	}
	if c.DurationSamples > 0 {
		v := c.sampleSeconds / float64(c.DurationSamples)
		c.MeanDurationSeconds = &v
		c.MedianDurationSeconds = percentile(c.histogram, c.DurationSamples, .5)
		c.P95DurationSeconds = percentile(c.histogram, c.DurationSamples, .95)
	}
}
func percentile(hist map[int]int64, samples int64, p float64) *float64 {
	if samples == 0 || len(hist) == 0 {
		return nil
	}
	buckets := make([]int, 0, len(hist))
	for b := range hist {
		buckets = append(buckets, b)
	}
	sort.Ints(buckets)
	rank := int64(math.Ceil(float64(samples) * p))
	var seen int64
	for _, b := range buckets {
		seen += hist[b]
		if seen >= rank {
			v := 0.0
			if b >= 0 {
				v = durationResolution * math.Pow(durationBucketRatio, float64(b))
			}
			return &v
		}
	}
	return nil
}

// Six equal rolling windows share one scan domain. Every query is worker-only;
// HTTP handlers merge already scoped aggregates and never access DuckDB.
func analyticsWindows(now time.Time) (string, []any) {
	values := []string{}
	args := []any{}
	for _, days := range []int{7, 30, 90} {
		for _, previous := range []bool{false, true} {
			end := now
			if previous {
				end = end.AddDate(0, 0, -days)
			}
			values = append(values, "(?,?,?,?)")
			args = append(args, days, previous, end.AddDate(0, 0, -days), end)
		}
	}
	return `WITH periods(days,previous,start_at,end_at) AS (VALUES ` + strings.Join(values, ",") + `), facts AS (
 SELECT p.days,p.previous,h.*,strftime(h.finished_at,'%Y-%m-%d') AS day,
 greatest(0,epoch(h.finished_at)-epoch(h.started_at)) AS elapsed,
 h.started_at>TIMESTAMP '0001-01-01 00:00:00' AND h.finished_at>=h.started_at AS valid_duration,
 h.state='succeeded' AND NOT h.reused AND h.started_at>TIMESTAMP '0001-01-01 00:00:00' AND h.finished_at>=h.started_at AS duration_sample
 FROM history h JOIN periods p ON h.finished_at>=p.start_at AND h.finished_at<p.end_at
 WHERE h.kind IN ('deployment','workflow','job')) `, args
}
func (s *Service) refresh(ctx context.Context, db *sql.DB, state string) error {
	return s.refreshAt(ctx, db, state, time.Now().UTC())
}
func (s *Service) refreshAt(ctx context.Context, db *sql.DB, state string, now time.Time) error {
	prefix, args := analyticsWindows(now)
	next := &snapshot{State: state, UpdatedAt: &now}
	if err := readCounts(ctx, db, prefix, args, next); err != nil {
		return err
	}
	if err := readHistograms(ctx, db, prefix, args, next); err != nil {
		return err
	}
	if err := readFailures(ctx, db, prefix, args, next); err != nil {
		return err
	}
	if err := readSlow(ctx, db, prefix, args, next); err != nil {
		return err
	}
	if err := readCoverage(ctx, db, now, next); err != nil {
		return err
	}
	s.current.Store(next)
	return nil
}
func readCounts(ctx context.Context, db *sql.DB, prefix string, args []any, next *snapshot) error {
	rows, err := db.QueryContext(ctx, prefix+`SELECT days,previous,project_id,day,kind,count(*),count(*) FILTER(WHERE state='succeeded'),count(*) FILTER(WHERE state='failed'),count(*) FILTER(WHERE state='cancelled'),count(*) FILTER(WHERE reused),coalesce(sum(elapsed) FILTER(WHERE valid_duration),0),count(*) FILTER(WHERE duration_sample),coalesce(sum(elapsed) FILTER(WHERE duration_sample),0) FROM facts GROUP BY 1,2,3,4,5`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r row
		c := &r.Counts
		if err = rows.Scan(&r.Days, &r.Previous, &r.Project, &r.Date, &r.Kind, &c.Runs, &c.Succeeded, &c.Failed, &c.Cancelled, &c.Reused, &c.DurationSeconds, &c.DurationSamples, &c.sampleSeconds); err != nil {
			return err
		}
		next.Rows = append(next.Rows, r)
	}
	return rows.Err()
}
func readHistograms(ctx context.Context, db *sql.DB, prefix string, args []any, next *snapshot) error {
	type key struct {
		Days                int
		Previous            bool
		Project, Date, Kind string
	}
	index := map[key]*Counts{}
	for i := range next.Rows {
		r := &next.Rows[i]
		index[key{r.Days, r.Previous, r.Project, r.Date, r.Kind}] = &r.Counts
	}
	rows, err := db.QueryContext(ctx, prefix+`SELECT days,previous,project_id,day,kind,CASE WHEN elapsed=0 THEN -1 ELSE greatest(0,ceil(ln(elapsed/0.01)/ln(1.05)))::INTEGER END AS bucket,count(*) FROM facts WHERE duration_sample GROUP BY 1,2,3,4,5,6`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var k key
		var bucket int
		var n int64
		if err = rows.Scan(&k.Days, &k.Previous, &k.Project, &k.Date, &k.Kind, &bucket, &n); err != nil {
			return err
		}
		if c := index[k]; c != nil {
			if c.histogram == nil {
				c.histogram = map[int]int64{}
			}
			c.histogram[bucket] = n
		}
	}
	return rows.Err()
}
func readFailures(ctx context.Context, db *sql.DB, prefix string, args []any, next *snapshot) error {
	rows, err := db.QueryContext(ctx, prefix+`, grouped AS (
 SELECT days,project_id,kind,name,count(*) AS runs,count(*) FILTER(WHERE state='succeeded') AS succeeded,count(*) FILTER(WHERE state='failed') AS failed,count(*) FILTER(WHERE state='cancelled') AS cancelled,max(finished_at) FILTER(WHERE state='failed') AS latest_failed_at,arg_max(entity_id,finished_at) FILTER(WHERE state='failed') AS latest_failed_id
 FROM facts WHERE NOT previous GROUP BY 1,2,3,4 HAVING count(*) FILTER(WHERE state='failed')>0)
 SELECT days,project_id,kind,name,runs,succeeded,failed,cancelled,latest_failed_at,latest_failed_id FROM grouped
 QUALIFY row_number() OVER(PARTITION BY days,project_id,kind ORDER BY failed DESC,failed::DOUBLE/(succeeded+failed) DESC,name)<=10`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v FailureHotspot
		var latest time.Time
		var id string
		if err = rows.Scan(&v.days, &v.ProjectID, &v.Kind, &v.Name, &v.Runs, &v.Succeeded, &v.Failed, &v.Cancelled, &latest, &id); err != nil {
			return err
		}
		latest = latest.UTC()
		v.LatestFailedAt = &latest
		v.FailureRate = 100 * float64(v.Failed) / float64(v.Succeeded+v.Failed)
		if v.Kind == "deployment" {
			v.LatestFailedDeploymentID = id
		}
		next.Failures = append(next.Failures, v)
	}
	return rows.Err()
}
func readSlow(ctx context.Context, db *sql.DB, prefix string, args []any, next *snapshot) error {
	rows, err := db.QueryContext(ctx, prefix+`, grouped AS (
 SELECT days,project_id,kind,name,count(*) AS runs,count(*) FILTER(WHERE duration_sample) AS samples,avg(elapsed) FILTER(WHERE duration_sample) AS mean_duration,arg_max(entity_id,finished_at) FILTER(WHERE duration_sample) AS latest_id
 FROM facts WHERE NOT previous GROUP BY 1,2,3,4 HAVING count(*) FILTER(WHERE duration_sample)>0)
 SELECT days,project_id,kind,name,runs,samples,mean_duration,latest_id FROM grouped
 QUALIFY row_number() OVER(PARTITION BY days,project_id,kind ORDER BY mean_duration DESC,samples DESC,name)<=10`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v SlowWorkload
		var id string
		if err = rows.Scan(&v.days, &v.ProjectID, &v.Kind, &v.Name, &v.Runs, &v.DurationSamples, &v.MeanDurationSeconds, &id); err != nil {
			return err
		}
		if v.Kind == "deployment" {
			v.LatestDeploymentID = id
		}
		next.Slow = append(next.Slow, v)
	}
	return rows.Err()
}
func readCoverage(ctx context.Context, db *sql.DB, now time.Time, next *snapshot) error {
	rows, err := db.QueryContext(ctx, `SELECT project_id,min(finished_at),max(finished_at) FROM history WHERE finished_at>TIMESTAMP '0001-01-01 00:00:00' AND finished_at<? AND kind IN ('deployment','workflow','job') GROUP BY 1`, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v projectCoverage
		var first, last time.Time
		if err = rows.Scan(&v.Project, &first, &last); err != nil {
			return err
		}
		first = first.UTC()
		last = last.UTC()
		v.Coverage = Coverage{FirstCompletedAt: &first, LastCompletedAt: &last}
		next.Coverage = append(next.Coverage, v)
	}
	return rows.Err()
}
