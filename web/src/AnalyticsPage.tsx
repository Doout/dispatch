import { useEffect, useState } from "react";
import { ArrowClockwise } from "@phosphor-icons/react";
import { api, type AnalyticsSummary } from "./api";
import { relative } from "./presentation";

export function AnalyticsPage() {
  const [days, setDays] = useState(30);
  const [attempt, setAttempt] = useState(0);
  const [data, setData] = useState<AnalyticsSummary>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let active = true;
    setLoading(true); setError(""); setData(undefined);
    // No polling or overview subscription: summaries load only on navigation,
    // range changes, or an explicit refresh.
    api.analytics(days).then(value => { if (active) setData(value); })
      .catch(cause => { if (active) setError(cause instanceof Error ? cause.message : "Could not load history."); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, [days, attempt]);
  const ready = data?.updatedAt;
  const totals = data?.totals;
  return <section className="analytics-page">
    <header className="page-header"><div><h1>Analytics</h1><p>Completed runs across your projects. Dates are in UTC.</p></div><div className="analytics-controls">
      <label>Period <select value={days} onChange={event => setDays(Number(event.target.value))}><option value={7}>Last 7 days</option><option value={30}>Last 30 days</option><option value={90}>Last 90 days</option></select></label>
      <button type="button" className="button secondary" disabled={loading} onClick={() => setAttempt(value => value + 1)}><ArrowClockwise size={16} />Refresh</button>
    </div></header>
    {loading && <p role="status">Loading history…</p>}
    {error && <p role="alert">{error}</p>}
    {data?.state === "disabled" && <p>Historical analytics is disabled on this installation.</p>}
    {data && !ready && data.state !== "disabled" && <p role="status">{data.state === "unavailable" ? "History is temporarily unavailable. Deployments are unaffected." : "Preparing historical data. Refresh shortly."}</p>}
    {ready && totals && <>
      <p className="analytics-updated">Updated {relative(ready)}{data.state === "catching_up" ? " · Importing older runs" : data.state === "unavailable" ? " · Updates paused; showing the last available summary" : ""}</p>
      <dl className="analytics-metrics">
        <div><dt>Deployments</dt><dd>{totals.deployments.runs}</dd><small>{totals.deployments.succeeded} succeeded · {totals.deployments.failed} failed · {totals.deployments.cancelled} cancelled</small></div>
        <div><dt>Average workflow duration</dt><dd>{totals.workflows.runs ? duration(totals.workflows.durationSeconds / totals.workflows.runs) : "—"}</dd><small>{totals.workflows.runs} completed workflows</small></div>
        <div><dt>Build reuse</dt><dd>{totals.jobs.runs ? `${Math.round(100 * totals.jobs.reused / totals.jobs.runs)}%` : "—"}</dd><small>{totals.jobs.reused} of {totals.jobs.runs} jobs reused</small></div>
      </dl>
      {totals.deployments.runs + totals.workflows.runs + totals.jobs.runs === 0 ? <p>No completed runs in this period.</p> : <div className="table-wrap"><table className="analytics-table"><caption>Daily activity</caption><thead><tr><th scope="col">Date</th><th scope="col">Deployments</th><th scope="col">Succeeded</th><th scope="col">Failed</th><th scope="col">Jobs</th><th scope="col">Reused</th><th scope="col">Avg. workflow</th></tr></thead><tbody>
        {[...data.daily].reverse().filter(day => day.deployments.runs + day.workflows.runs + day.jobs.runs > 0).map(day => <tr key={day.date}><th scope="row">{day.date}</th><td>{day.deployments.runs}</td><td>{day.deployments.succeeded}</td><td>{day.deployments.failed}</td><td>{day.jobs.runs}</td><td>{day.jobs.reused}</td><td>{day.workflows.runs ? duration(day.workflows.durationSeconds / day.workflows.runs) : "—"}</td></tr>)}
      </tbody></table></div>}
    </>}
  </section>;
}
function duration(seconds: number) { return seconds < 60 ? `${Math.round(seconds)}s` : `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`; }
