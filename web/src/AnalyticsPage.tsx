import { useEffect, useState, type ReactNode } from "react";
import { ArrowClockwise, ArrowDown, ArrowRight, ArrowUp, ChartBar, CheckCircle, Clock, Info, WarningCircle } from "@phosphor-icons/react";
import { type Overview } from "./api";
import { relative } from "./presentation";
import { type AnalyticsFilters, type AppRoute, routePath, shouldHandleNavigation } from "./routes";
import { OutcomeChart, DurationChart } from "./analytics/Charts";
import { analyticsClient, type AnalyticsCounts, type AnalyticsDashboard, type AnalyticsKind, type AnalyticsPeriod, countKey, dayLabel, formatCount, formatDuration, formatRate, kindLabel } from "./analytics/client";
import "./AnalyticsPage.css";

type Navigate = (route: AppRoute) => void;
const kinds: AnalyticsKind[] = ["deployment", "workflow", "job"];
const singular = { deployment: "deployment", workflow: "workflow", job: "job" };
function calendarTime(value: string) { return new Date(value).toLocaleString("en", { month: "short", day: "numeric", hour: "numeric", minute: "2-digit", timeZone: "UTC" }); }
function periodLabel(period: AnalyticsPeriod) { return `${calendarTime(period.start)} – ${calendarTime(period.end)} UTC`; }

export function AnalyticsPage({ overview, onNavigate, filters, onFilters }: { overview?: Overview; onNavigate?: Navigate; filters?: AnalyticsFilters; onFilters?: (filters: AnalyticsFilters) => void }) {
  const [localFilters, setLocalFilters] = useState<AnalyticsFilters>(filters ?? {});
  const selected = onFilters ? filters ?? {} : localFilters;
  const days = selected.days ?? 30;
  const project = selected.projectId ?? "";
  const kind = selected.kind ?? "deployment";
  const tab = selected.section ?? "overview";
  function change(patch: Partial<AnalyticsFilters>) { const next = { ...selected, ...patch }; if (onFilters) onFilters(next); else setLocalFilters(next); }
  const setDays = (days: number) => change({ days: days as 7 | 30 | 90 });
  const setProject = (projectId: string) => change({ projectId });
  const setKind = (kind: AnalyticsKind) => change({ kind });
  const setTab = (section: "overview" | "data") => change({ section });
  const [attempt, setAttempt] = useState(0);
  const [data, setData] = useState<AnalyticsDashboard>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let active = true;
    setLoading(true); setError(""); setData(undefined);
    // Historical summaries refresh only when requested; live overview updates
    // must not repeatedly query the historical read model.
    analyticsClient.summary(days, project).then(value => { if (active) setData(value); })
      .catch(cause => { if (active) setError(cause instanceof Error ? cause.message : "Could not load analytics."); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [days, project, attempt]);
  const ready = data?.updatedAt && data?.totals && data.state !== "disabled";
  return <section className="analytics-dashboard page-layout">
    <header className="page-header analytics-header"><div><h1>Analytics</h1><p>See where delivery succeeds, fails, and slows down.</p></div><button type="button" className="quiet-button" disabled={loading} onClick={() => setAttempt(value => value + 1)}><ArrowClockwise size={16} />{loading ? "Loading…" : "Refresh"}</button></header>
    <div className="analytics-filter-bar" aria-label="Analytics filters">
      <label htmlFor="analytics-period">Period<select id="analytics-period" value={days} onChange={event => setDays(Number(event.target.value))}><option value={7}>Last 7 days</option><option value={30}>Last 30 days</option><option value={90}>Last 90 days</option></select></label>
      <label htmlFor="analytics-project">Project<select id="analytics-project" value={project} onChange={event => setProject(event.target.value)}><option value="">All visible projects</option>{overview?.projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <div className="analytics-kind-switch" role="group" aria-label="Workload type">{kinds.map(value => <button key={value} type="button" aria-pressed={kind === value} onClick={() => setKind(value)}>{kindLabel[value][0].toUpperCase() + kindLabel[value].slice(1)}</button>)}</div>
    </div>
    {loading && <div className="analytics-state" role="status"><ChartBar size={24} /><strong>Loading analytics…</strong><span>Reading completed run history.</span></div>}
    {error && <div className="analytics-state analytics-error" role="alert"><WarningCircle size={24} /><strong>Analytics could not be loaded</strong><span>{error}</span><button className="quiet-button" onClick={() => setAttempt(value => value + 1)}>Try again</button></div>}
    {data?.state === "disabled" && <div className="analytics-state"><ChartBar size={24} /><strong>Historical analytics is disabled</strong><span>Enable analytics on this installation to collect delivery trends.</span></div>}
    {data && !ready && data.state !== "disabled" && <div className="analytics-state" role="status"><Clock size={24} /><strong>{data.state === "unavailable" ? "History is temporarily unavailable" : "Preparing historical data"}</strong><span>{data.state === "unavailable" ? "Deployments are unaffected. Refresh to try again." : "Completed runs are being imported. Refresh shortly."}</span></div>}
    {ready && <>
      <div className="analytics-snapshot"><span title={new Date(data.updatedAt!).toLocaleString()}>Updated {relative(data.updatedAt!)}</span>{data.period && <span>{periodLabel(data.period)}</span>}</div>
      {data.state === "catching_up" && <p className="analytics-notice" role="status"><Info size={16} />Importing older runs. Counts may change as history arrives; period comparisons are paused.</p>}
      {data.state === "unavailable" && <p className="analytics-notice" role="status"><WarningCircle size={16} />Updates paused; showing the last available summary.</p>}
      <DashboardContent data={data} kind={kind} project={project} overview={overview} tab={tab} setTab={setTab} onNavigate={onNavigate} />
    </>}
  </section>;
}

function DashboardContent({ data, kind, project, overview, tab, setTab, onNavigate }: { data: AnalyticsDashboard; kind: AnalyticsKind; project: string; overview?: Overview; tab: "overview" | "data"; setTab: (tab: "overview" | "data") => void; onNavigate?: Navigate }) {
  const totals = data.totals[countKey[kind]];
  const previous = data.state === "catching_up" ? undefined : data.previousTotals?.[countKey[kind]];
  const hotspots = (data.failureHotspots ?? []).filter(item => item.kind === kind && item.failed > 0).slice(0, 6);
  const slow = (data.slowWorkloads ?? []).filter(item => item.kind === kind && item.durationSamples > 0).slice(0, 6);
  const projectName = (id: string) => overview?.projects.find(item => item.id === id)?.name ?? "Project";
  function deploymentRoute(status?: string, period = data.period): AppRoute {
    return { view: "deployments", deploymentFilters: { layout: "list", project: project || undefined, status, ...(period ? { completedFrom: period.start, completedTo: period.end } : {}) } };
  }
  function openDay(date: string) {
    if (!data.period || !onNavigate) return;
    const dayStart = new Date(`${date}T00:00:00Z`);
    const nextDay = new Date(dayStart.getTime() + 86400000);
    const start = Date.parse(data.period.start) >= dayStart.getTime() ? data.period.start : dayStart.toISOString();
    const end = Date.parse(data.period.end) <= nextDay.getTime() ? data.period.end : nextDay.toISOString();
    onNavigate(deploymentRoute(undefined, { start, end }));
  }
  const compareTitle = data.previousPeriod ? `Previous period: ${periodLabel(data.previousPeriod)}` : undefined;
  return <>
    <dl className="analytics-summary-strip" aria-label={`${kindLabel[kind]} summary`}>
      <Metric label={`Completed ${kindLabel[kind]}`} value={formatCount(totals.runs)} icon={<ChartBar size={16} />} note={`${formatCount(totals.succeeded)} succeeded · ${formatCount(totals.failed)} failed · ${formatCount(totals.cancelled)} cancelled`} comparison={<Comparison current={totals.runs} previous={previous?.runs} type="volume" title={compareTitle} />} />
      <Metric label="Success rate" value={formatRate(totals.successRate)} icon={<CheckCircle size={16} />} note="Succeeded ÷ succeeded and failed; cancellations excluded" comparison={<Comparison current={totals.successRate} previous={previous?.successRate} type="rate" title={compareTitle} />} />
      <Metric label="Median duration" value={formatDuration(totals.medianDurationSeconds)} icon={<Clock size={16} />} note={`Estimated · ${formatCount(totals.durationSamples)} successful${kind === "job" ? ", non-reused" : ""} samples`} comparison={<Comparison current={totals.medianDurationSeconds} previous={previous?.medianDurationSeconds} type="duration" title={compareTitle} />} />
      <Metric label="p95 duration" value={formatDuration(totals.p95DurationSeconds)} icon={<Clock size={16} />} note="Estimated upper end of successful run durations" comparison={<Comparison current={totals.p95DurationSeconds} previous={previous?.p95DurationSeconds} type="duration" title={compareTitle} />} />
    </dl>
    {kind === "job" && totals.runs > 0 && <p className="analytics-reuse"><strong>{formatCount(totals.reused)}</strong> of {formatCount(totals.runs)} completed jobs reused a prior result. Reused jobs are excluded from duration metrics.</p>}
    <div className="analytics-section-switch"><div role="tablist" aria-label="Analytics view">{(["overview", "data"] as const).map(value => <button key={value} id={`analytics-tab-${value}`} role="tab" aria-selected={tab === value} aria-controls={`analytics-panel-${value}`} tabIndex={tab === value ? 0 : -1} onKeyDown={event => { if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return; event.preventDefault(); const next = event.key === "Home" ? "overview" : event.key === "End" ? "data" : tab === "overview" ? "data" : "overview"; setTab(next); document.getElementById(`analytics-tab-${next}`)?.focus(); }} onClick={() => setTab(value)}>{value === "overview" ? "Trends & insights" : "Data"}</button>)}</div>{kind === "deployment" && <AnalyticsLink route={deploymentRoute()} onNavigate={onNavigate}>View retained deployments<ArrowRight size={14} /></AnalyticsLink>}</div>
    {!totals.runs ? <div className="analytics-state" role="status"><ChartBar size={24} /><strong>No completed {kindLabel[kind]} in this period</strong><span>Try a longer period or another project. In-progress runs are not counted.</span></div> : tab === "data" ? <div role="tabpanel" id="analytics-panel-data" aria-labelledby="analytics-tab-data"><DailyData data={data} kind={kind} /></div> : <div role="tabpanel" id="analytics-panel-overview" aria-labelledby="analytics-tab-overview">
      <div className="analytics-trends">
        <section className="analytics-panel"><header><div><h2>Delivery outcomes</h2><p>Completed {kindLabel[kind]} per UTC day</p></div>{totals.failed > 0 && kind === "deployment" && <AnalyticsLink route={deploymentRoute("failed")} onNavigate={onNavigate}>{formatCount(totals.failed)} failures<ArrowRight size={13} /></AnalyticsLink>}</header><div className="analytics-legend"><span><i className="succeeded" />Succeeded</span><span><i className="failed" />Failed</span><span><i className="cancelled" />Cancelled</span></div><OutcomeChart days={data.daily ?? []} kind={kind} onDay={kind === "deployment" && data.period && onNavigate ? openDay : undefined} /></section>
        <section className="analytics-panel"><header><div><h2>Duration trend</h2><p>Successful runs with recorded timing</p></div></header><div className="analytics-legend"><span><i className="median" />Median · estimated</span><span><i className="p95" />p95 · estimated</span></div><DurationChart days={data.daily ?? []} kind={kind} /></section>
      </div>
      <div className="analytics-insights">
        <section className="analytics-panel"><header><div><h2>Failure hotspots</h2><p>Workloads with the most failed {kindLabel[kind]}</p></div><WarningCircle size={17} className="analytics-muted" /></header>{!hotspots.length ? <div className="analytics-insight-empty"><CheckCircle size={18} /><span>No failed {kindLabel[kind]} recorded in this period.</span></div> : <ol className="analytics-rank-list">{hotspots.map((item, index) => <li key={`${item.projectId}/${item.name}`}><span className="analytics-rank">{index + 1}</span><div className="analytics-workload"><strong>{item.name || `Unnamed ${singular[kind]}`}</strong><span>{projectName(item.projectId)} · {formatCount(item.runs)} completed runs</span>{item.latestFailedDeploymentId && kind === "deployment" && <AnalyticsLink route={{ view: "deployments", deploymentID: item.latestFailedDeploymentId }} onNavigate={onNavigate}>Inspect last failure<ArrowRight size={12} /></AnalyticsLink>}</div><div className="analytics-rank-value analytics-failure-value"><strong>{formatCount(item.failed)} failed</strong><span>{formatRate(item.failureRate)} failure rate</span></div></li>)}</ol>}</section>
        <section className="analytics-panel"><header><div><h2>Slow workloads</h2><p>Highest mean duration among successful runs</p></div><Clock size={17} className="analytics-muted" /></header>{!slow.length ? <div className="analytics-insight-empty"><Clock size={18} /><span>No successful runs with recorded timing.</span></div> : <ol className="analytics-rank-list">{slow.map((item, index) => <li key={`${item.projectId}/${item.name}`}><span className="analytics-rank">{index + 1}</span><div className="analytics-workload"><strong>{item.name || `Unnamed ${singular[kind]}`}</strong><span>{projectName(item.projectId)} · {formatCount(item.durationSamples)} timed successful runs</span>{item.latestDeploymentId && kind === "deployment" && <AnalyticsLink route={{ view: "deployments", deploymentID: item.latestDeploymentId }} onNavigate={onNavigate}>Open timed success<ArrowRight size={12} /></AnalyticsLink>}</div><div className="analytics-rank-value"><strong>{formatDuration(item.meanDurationSeconds)}</strong><span>mean duration</span></div></li>)}</ol>}</section>
      </div>
    </div>}
    <details className="analytics-method"><summary><Info size={14} />How these metrics are calculated</summary><p>Counts include completed runs whose completion timestamp falls inside the selected rolling period. The first and last daily buckets may be partial UTC days. Comparisons use the preceding period of the same length.</p><p>Recorded elapsed duration uses successful runs with valid recorded start and finish times; reused jobs are excluded. Older records may include waiting or zero durations when original timestamps were unavailable. Median and p95 are histogram estimates{data.capabilities ? ` with up to ${data.capabilities.durationPercentileMaxErrorPercent}% bucket rounding and ${data.capabilities.durationPercentileResolutionSeconds * 1000}ms minimum resolution` : ""}. They are not deployment-stage timings.</p><p>Workloads are grouped by the names recorded in history. Renames can create separate groups. Retained history does not record environment associations. Historical counts can include removed runs. The deployment list shows retained records; individual archived run links may be unavailable after cleanup.</p>{data.coverage?.firstCompletedAt && <p>Earliest recorded completion: {calendarTime(data.coverage.firstCompletedAt)} UTC.</p>}</details>
  </>;
}
function Metric({ label, value, note, icon, comparison }: { label: string; value: string; note: string; icon: ReactNode; comparison: ReactNode }) {
  return <div className="analytics-summary-metric"><dt>{icon}{label}</dt><dd>{value}</dd>{comparison}<small>{note}</small></div>;
}
function Comparison({ current, previous, type, title }: { current: number | null | undefined; previous: number | null | undefined; type: "volume" | "rate" | "duration"; title?: string }) {
  if (current == null || previous == null || !Number.isFinite(current) || !Number.isFinite(previous)) return <span className="analytics-comparison muted" title={title}>No comparable previous sample</span>;
  if (previous === 0 && type !== "rate") return <span className="analytics-comparison muted" title={title}>{current === 0 ? "No change from previous period" : "No previous-period baseline"}</span>;
  const delta = type === "rate" ? current - previous : (current - previous) / previous * 100;
  const absolute = Math.abs(delta);
  if (absolute < 0.05) return <span className="analytics-comparison muted" title={title}>No change from previous period</span>;
  const better = type === "rate" ? delta > 0 : delta < 0;
  const Icon = delta > 0 ? ArrowUp : ArrowDown;
  return <span className={`analytics-comparison ${type === "volume" ? "muted" : better ? "better" : "worse"}`} title={title}><Icon size={12} />{absolute.toFixed(1)}{type === "rate" ? " pp" : "%"} {delta > 0 ? "higher" : "lower"}<span className="analytics-comparison-context">vs previous period</span></span>;
}
function AnalyticsLink({ route, onNavigate, children }: { route: AppRoute; onNavigate?: Navigate; children: ReactNode }) {
  return <a className="analytics-link" href={routePath(route)} onClick={event => { if (onNavigate && shouldHandleNavigation(event)) { event.preventDefault(); onNavigate(route); } }}>{children}</a>;
}
function DailyData({ data, kind }: { data: AnalyticsDashboard; kind: AnalyticsKind }) {
  return <section className="analytics-panel analytics-data"><header><div><h2>Daily {kindLabel[kind]}</h2><p>Completed counts and estimated successful-run duration. All dates are UTC.</p></div></header><div className="analytics-data-scroll"><table><caption className="sr-only">Daily {kindLabel[kind]} counts and estimated timing</caption><thead><tr><th scope="col">Date</th><th scope="col">Completed</th><th scope="col">Succeeded</th><th scope="col">Failed</th><th scope="col">Cancelled</th><th scope="col">Success</th><th scope="col">Median</th><th scope="col">p95</th><th scope="col">Samples</th></tr></thead><tbody>{[...(data.daily ?? [])].reverse().map(day => { const count: AnalyticsCounts = day[countKey[kind]]; return <tr key={day.date}><th scope="row"><time dateTime={day.date}>{dayLabel(day.date)}</time></th><td>{formatCount(count.runs)}</td><td>{formatCount(count.succeeded)}</td><td>{formatCount(count.failed)}</td><td>{formatCount(count.cancelled)}</td><td>{formatRate(count.successRate)}</td><td>{formatDuration(count.medianDurationSeconds)}</td><td>{formatDuration(count.p95DurationSeconds)}</td><td>{formatCount(count.durationSamples)}</td></tr>; })}</tbody></table></div></section>;
}
