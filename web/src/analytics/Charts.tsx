import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { type AnalyticsDay, type AnalyticsKind, countKey, dayLabel, formatCount, formatDuration } from "./client";

const height = 232;
const left = 48;
const right = 16;
const top = 16;
const bottom = 36;
const plotHeight = height - top - bottom;
const baseline = top + plotHeight;
const x = (index: number, count: number, width: number) => left + (index + 0.5) * (width - left - right) / Math.max(count, 1);
const y = (value: number, maximum: number) => baseline - plotHeight * value / maximum;
function useChartWidth() {
  const container = useRef<HTMLElement | null>(null);
  const [width, setWidth] = useState(720);
  useEffect(() => {
    if (!container.current || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(entries => setWidth(Math.max(260, entries[0].contentRect.width)));
    observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  return { container, width, plotWidth: width - left - right };
}
function axisDates(days: AnalyticsDay[]) {
  return [...new Set([0, Math.floor((days.length - 1) / 3), Math.floor(2 * (days.length - 1) / 3), days.length - 1])].filter(i => i >= 0);
}
function Grid({ maximum, width, duration = false }: { maximum: number; width: number; duration?: boolean }) {
  return <g aria-hidden="true" className="analytics-chart-grid">{[0, 0.5, 1].map(fraction => <g key={fraction}>
    <line x1={left} x2={width - right} y1={y(maximum * fraction, maximum)} y2={y(maximum * fraction, maximum)} />
    <text x={left - 9} y={y(maximum * fraction, maximum) + 4} textAnchor="end">{duration ? formatDuration(maximum * fraction) : formatCount(maximum * fraction)}</text>
  </g>)}</g>;
}
function DateAxis({ days, width }: { days: AnalyticsDay[]; width: number }) {
  return <g aria-hidden="true" className="analytics-chart-axis">{axisDates(days).map(index => <text key={index} x={x(index, days.length, width)} y={height - 9} textAnchor={index === 0 ? "start" : index === days.length - 1 ? "end" : "middle"}>{dayLabel(days[index].date)}</text>)}</g>;
}
export function OutcomeChart({ days, kind, onDay }: { days: AnalyticsDay[]; kind: AnalyticsKind; onDay?: (date: string) => void }) {
  const { container, width, plotWidth } = useChartWidth();
  const titleID = useId();
  const instructionID = useId();
  const [selected, setSelected] = useState<number | null>(null);
  const [focused, setFocused] = useState(0);
  const bars = useRef<(SVGGElement | null)[]>([]);
  useEffect(() => { setSelected(null); setFocused(0); }, [days, kind]);
  const key = countKey[kind];
  const maximum = Math.max(2, Math.ceil(Math.max(...days.map(day => day[key].runs), 0) / 2) * 2);
  const barWidth = Math.max(2, plotWidth / Math.max(1, days.length) * 0.67);
  const active = selected == null ? undefined : days[selected];
  function move(event: KeyboardEvent<SVGGElement>, index: number) {
    const next = event.key === "ArrowRight" ? Math.min(index + 1, days.length - 1) : event.key === "ArrowLeft" ? Math.max(0, index - 1) : event.key === "Home" ? 0 : event.key === "End" ? days.length - 1 : undefined;
    if (next != null) { event.preventDefault(); setFocused(next); setSelected(next); bars.current[next]?.focus?.(); }
    if ((event.key === "Enter" || event.key === " ") && onDay) { event.preventDefault(); onDay(days[index].date); }
  }
  return <figure className="analytics-chart" ref={container}>
    <svg viewBox={`0 0 ${width} ${height}`} role="group" aria-labelledby={titleID} aria-describedby={instructionID}>
      <title id={titleID}>Daily completed {kind === "deployment" ? "deployments" : kind === "workflow" ? "workflows" : "jobs"} by outcome</title>
      <desc id={instructionID}>Each bar shows succeeded, failed, and cancelled runs on a UTC day. Use left and right arrow keys to inspect days.{onDay ? " Press Enter to view that day's deployments." : ""} The Data tab provides a table.</desc>
      <Grid width={width} maximum={maximum} />
      {days.map((day, index) => {
        const count = day[key];
        let accumulated = 0;
        const label = `${dayLabel(day.date)}: ${count.succeeded} succeeded, ${count.failed} failed, ${count.cancelled} cancelled${onDay ? ". View deployments" : ""}`;
        return <g key={day.date} ref={element => { bars.current[index] = element; }} role={onDay ? "button" : "img"} aria-label={label} tabIndex={focused === index ? 0 : -1} className="analytics-chart-day" onFocus={() => { setSelected(index); setFocused(index); }} onMouseEnter={() => setSelected(index)} onClick={() => onDay?.(day.date)} onKeyDown={event => move(event, index)}>
          <rect className="analytics-chart-hit" x={x(index, days.length, width) - plotWidth / days.length / 2} y={top} width={plotWidth / days.length} height={plotHeight} />
          {(["succeeded", "failed", "cancelled"] as const).map(outcome => {
            const value = count[outcome]; accumulated += value;
            return <rect key={outcome} className={`analytics-bar-${outcome}`} x={x(index, days.length, width) - barWidth / 2} y={y(accumulated, maximum)} width={barWidth} height={plotHeight * value / maximum} />;
          })}
        </g>;
      })}
      <DateAxis width={width} days={days} />
    </svg>
    <figcaption className="analytics-chart-reading" aria-live="polite">{active ? <><strong>{dayLabel(active.date)}</strong><span>{formatCount(active[key].succeeded)} succeeded</span><span>{formatCount(active[key].failed)} failed</span><span>{formatCount(active[key].cancelled)} cancelled</span></> : <span>Point to a day or use the arrow keys to inspect its outcomes.</span>}</figcaption>
  </figure>;
}
export function DurationChart({ days, kind }: { days: AnalyticsDay[]; kind: AnalyticsKind }) {
  const { container, width, plotWidth } = useChartWidth();
  const titleID = useId();
  const key = countKey[kind];
  const maximum = Math.max(0.01, ...days.map(day => day[key].p95DurationSeconds ?? 0));
  const hasSamples = days.some(day => day[key].durationSamples > 0);
  const [selected, setSelected] = useState<number | null>(null);
  const [focused, setFocused] = useState(0);
  const points = useRef<(SVGRectElement | null)[]>([]);
  useEffect(() => { setSelected(null); setFocused(0); }, [days, kind]);
  function move(event: KeyboardEvent<SVGRectElement>, index: number) {
    const next = event.key === "ArrowRight" ? Math.min(index + 1, days.length - 1) : event.key === "ArrowLeft" ? Math.max(0, index - 1) : event.key === "Home" ? 0 : event.key === "End" ? days.length - 1 : undefined;
    if (next != null) { event.preventDefault(); setFocused(next); setSelected(next); points.current[next]?.focus?.(); }
  }
  function segments(field: "medianDurationSeconds" | "p95DurationSeconds") {
    const paths: string[] = []; let path: string[] = [];
    days.forEach((day, index) => {
      const value = day[key][field];
      if (value == null) { if (path.length) paths.push(path.join(" ")); path = []; }
      else path.push(`${x(index, days.length, width)},${y(value, maximum)}`);
    });
    if (path.length) paths.push(path.join(" "));
    return paths;
  }
  const active = selected == null ? undefined : days[selected];
  return <figure className="analytics-chart" ref={container}>
    {!hasSamples ? <div className="analytics-chart-empty">No successful runs with recorded duration in this period.</div> : <svg viewBox={`0 0 ${width} ${height}`} role="group" aria-labelledby={titleID}>
      <title id={titleID}>Estimated median and p95 duration for successful {kind === "job" ? "non-reused jobs" : `${kind}s`} by UTC day. Gaps indicate days without duration samples. Use left and right arrow keys to inspect days, or see the Data tab for values.</title>
      <Grid width={width} maximum={maximum} duration />
      {(["p95DurationSeconds", "medianDurationSeconds"] as const).map(field => <g key={field} aria-hidden="true" className={field === "medianDurationSeconds" ? "analytics-duration-median" : "analytics-duration-p95"}>{segments(field).map((points, index) => <polyline key={index} points={points} />)}{days.map((day, index) => day[key][field] == null ? null : <circle key={day.date} cx={x(index, days.length, width)} cy={y(day[key][field]!, maximum)} r={days.length < 40 ? 2.5 : 1.5} />)}</g>)}
      {days.map((day, index) => <rect key={day.date} ref={element => { points.current[index] = element; }} role="img" aria-label={`${dayLabel(day.date)}: median ${formatDuration(day[key].medianDurationSeconds)}, p95 ${formatDuration(day[key].p95DurationSeconds)}, ${day[key].durationSamples} duration samples`} tabIndex={focused === index ? 0 : -1} onFocus={() => { setSelected(index); setFocused(index); }} onKeyDown={event => move(event, index)} className="analytics-chart-hit analytics-duration-hit" x={x(index, days.length, width) - plotWidth / days.length / 2} y={top} width={plotWidth / days.length} height={plotHeight} onMouseEnter={() => setSelected(index)}><title>{dayLabel(day.date)}: median {formatDuration(day[key].medianDurationSeconds)}, p95 {formatDuration(day[key].p95DurationSeconds)}, {day[key].durationSamples} samples</title></rect>)}
      <DateAxis width={width} days={days} />
    </svg>}
    <figcaption className="analytics-chart-reading">{active ? <><strong>{dayLabel(active.date)}</strong><span>Median {formatDuration(active[key].medianDurationSeconds)}</span><span>p95 {formatDuration(active[key].p95DurationSeconds)}</span><span>{formatCount(active[key].durationSamples)} samples</span></> : <span>Successful runs only. Missing samples appear as gaps.</span>}</figcaption>
  </figure>;
}
