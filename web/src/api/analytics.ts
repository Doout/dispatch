import { request } from "./transport";

export type AnalyticsCounts = { runs: number; succeeded: number; failed: number; cancelled: number; reused: number; durationSeconds: number };

export type AnalyticsDay = { date: string; deployments: AnalyticsCounts; workflows: AnalyticsCounts; jobs: AnalyticsCounts };

export type AnalyticsSummary = { state: string; updatedAt?: string; days: number; daily: AnalyticsDay[]; totals: AnalyticsDay };

export const analyticsApi = {
  analytics: (days: number) => request<AnalyticsSummary>(`/api/v1/analytics?days=${days}`),
};
