import { request } from "../api";

export type AnalyticsKind = "deployment" | "workflow" | "job";
export type AnalyticsCounts = {
  runs: number;
  succeeded: number;
  failed: number;
  cancelled: number;
  reused: number;
  durationSeconds: number;
  successRate: number | null;
  durationSamples: number;
  meanDurationSeconds: number | null;
  medianDurationSeconds: number | null;
  p95DurationSeconds: number | null;
};
export type AnalyticsDay = {
  date: string;
  deployments: AnalyticsCounts;
  workflows: AnalyticsCounts;
  jobs: AnalyticsCounts;
};
export type AnalyticsPeriod = { start: string; end: string };
export type FailureHotspot = {
  projectId: string;
  kind: AnalyticsKind;
  name: string;
  runs: number;
  succeeded: number;
  failed: number;
  cancelled: number;
  failureRate: number;
  latestFailedAt?: string;
  latestFailedDeploymentId?: string;
};
export type SlowWorkload = {
  projectId: string;
  kind: AnalyticsKind;
  name: string;
  runs: number;
  durationSamples: number;
  meanDurationSeconds: number;
  latestDeploymentId?: string;
};
export type AnalyticsDashboard = {
  state: string;
  updatedAt?: string;
  days: number;
  daily: AnalyticsDay[];
  totals: AnalyticsDay;
  previousTotals?: AnalyticsDay;
  period?: AnalyticsPeriod;
  previousPeriod?: AnalyticsPeriod;
  failureHotspots?: FailureHotspot[];
  slowWorkloads?: SlowWorkload[];
  capabilities?: {
    durationPercentiles: "histogram";
    durationPercentileMaxErrorPercent: number;
    durationPercentileResolutionSeconds: number;
    durationPopulation: "successful_non_reused";
    stageTiming: boolean;
    environmentBreakdown: boolean;
  };
  coverage?: { firstCompletedAt?: string; lastCompletedAt?: string };
};
export const analyticsClient = {
  summary: (days: number, projectId = "") => {
    const query = new URLSearchParams({ days: String(days) });
    if (projectId) query.set("projectId", projectId);
    return request<AnalyticsDashboard>(`/api/v1/analytics?${query}`);
  },
};
export const countKey = { deployment: "deployments", workflow: "workflows", job: "jobs" } as const;
export const kindLabel = { deployment: "deployments", workflow: "workflows", job: "jobs" } as const;
export function formatDuration(seconds: number | null | undefined) {
  if (seconds == null || !Number.isFinite(seconds)) return "—";
  if (seconds < 1) return `${Math.round(seconds * 1000)}ms`;
  const rounded = Math.round(seconds);
  if (rounded < 60) return `${rounded}s`;
  if (rounded < 3600) return `${Math.floor(rounded / 60)}m${rounded % 60 ? ` ${rounded % 60}s` : ""}`;
  const minutes = Math.round(rounded / 60);
  return `${Math.floor(minutes / 60)}h${minutes % 60 ? ` ${minutes % 60}m` : ""}`;
}
export const formatCount = (count: number) => new Intl.NumberFormat("en", { maximumFractionDigits: 0 }).format(count);
export const formatRate = (rate: number | null | undefined) => rate == null || !Number.isFinite(rate) ? "—" : `${rate.toFixed(1)}%`;
export function dayLabel(date: string) {
  return new Date(`${date}T00:00:00Z`).toLocaleDateString("en", { month: "short", day: "numeric", timeZone: "UTC" });
}
