// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { useState } from "react";
import { AnalyticsPage } from "./AnalyticsPage";
import { type Overview } from "./api";
import { type AnalyticsFilters, routePath } from "./routes";
import { analyticsClient, type AnalyticsCounts, type AnalyticsDashboard, formatDuration } from "./analytics/client";

const zero: AnalyticsCounts = { runs: 0, succeeded: 0, failed: 0, cancelled: 0, reused: 0, durationSeconds: 0, successRate: null, durationSamples: 0, meanDurationSeconds: null, medianDurationSeconds: null, p95DurationSeconds: null };
const counts: AnalyticsCounts = { ...zero, runs: 12, succeeded: 8, failed: 2, cancelled: 2, successRate: 80, durationSamples: 8, meanDurationSeconds: 80, medianDurationSeconds: 60, p95DurationSeconds: 180 };
const totals = { date: "", deployments: counts, workflows: { ...counts, runs: 3, succeeded: 1, failed: 2, cancelled: 0, durationSamples: 1 }, jobs: { ...counts, reused: 4, durationSamples: 4 } };
const previous = { ...totals, deployments: { ...counts, runs: 6, successRate: 66.7, medianDurationSeconds: 120, p95DurationSeconds: 240 } };
const daily = ["2026-08-20", "2026-08-21", "2026-09-19"].map((date, index) => ({ ...totals, date, deployments: index === 1 ? zero : { ...counts, runs: 6, succeeded: 4, failed: 1, cancelled: 1, durationSamples: 4 } }));
const data: AnalyticsDashboard = {
 state: "ready", days: 30, updatedAt: "2026-09-19T12:00:00Z", period: { start: "2026-08-20T12:00:00Z", end: "2026-09-19T12:00:00Z" }, previousPeriod: { start: "2026-07-21T12:00:00Z", end: "2026-08-20T12:00:00Z" }, daily, totals, previousTotals: previous,
 failureHotspots: [{ projectId: "p", kind: "deployment", name: "Checkout production", runs: 12, succeeded: 8, failed: 2, cancelled: 2, failureRate: 20, latestFailedDeploymentId: "failed-run" }, { projectId: "p", kind: "workflow", name: "Workflow failure", runs: 3, succeeded: 1, failed: 2, cancelled: 0, failureRate: 66.7 }],
 slowWorkloads: [{ projectId: "p", kind: "deployment", name: "Checkout production", runs: 12, durationSamples: 8, meanDurationSeconds: 80, latestDeploymentId: "latest-run" }], capabilities: { durationPercentiles: "histogram", durationPercentileMaxErrorPercent: 5, durationPercentileResolutionSeconds: 0.01, durationPopulation: "successful_non_reused", stageTiming: false, environmentBreakdown: false },
};
const overview = { identity: { id: "viewer", systemRole: "member" }, projects: [{ id: "p", name: "Platform" }, { id: "other", name: "Other project" }] } as unknown as Overview;
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("shows meaningful outcomes and honest percentile comparisons before the raw daily table", async () => {
 vi.spyOn(analyticsClient, "summary").mockResolvedValue(data);
 render(<AnalyticsPage overview={overview} />);
 await screen.findByRole("heading", { name: "Delivery outcomes" });
 const metrics = screen.getByLabelText("deployments summary");
 expect(within(metrics).getByText("80.0%")).toBeTruthy();
 expect(within(metrics).getByText("1m")).toBeTruthy();
 expect(within(metrics).getByText(/13.3 pp higher/)).toBeTruthy();
 expect(within(metrics).getByText(/50.0% lower/)).toBeTruthy();
 expect(within(metrics).getByText(/Estimated · 8 successful samples/)).toBeTruthy();
 expect(screen.getByRole("heading", { name: "Failure hotspots" })).toBeTruthy();
 expect(screen.getByRole("heading", { name: "Slow workloads" })).toBeTruthy();
 expect(screen.queryByRole("table")).toBeNull();
 await userEvent.setup().click(screen.getByRole("tab", { name: "Data" }));
 expect(screen.getByRole("table", { name: /Daily deployments counts/ })).toBeTruthy();
});

it("fetches only for range, project, and explicit refresh while switching workloads locally", async () => {
 const request = vi.spyOn(analyticsClient, "summary").mockResolvedValue(data);
 const user = userEvent.setup(); render(<AnalyticsPage overview={overview} />);
 await screen.findByRole("heading", { name: "Delivery outcomes" });
 await user.click(screen.getByRole("button", { name: "Workflows" }));
 expect(request).toHaveBeenCalledTimes(1);
 expect(screen.getByText("Workflow failure")).toBeTruthy();
 expect(screen.queryByRole("link", { name: "View retained deployments" })).toBeNull();
 expect(screen.queryByRole("link", { name: "Inspect last failure" })).toBeNull();
 await user.selectOptions(screen.getByLabelText("Period", { exact: true }), "7");
 await waitFor(() => expect(request).toHaveBeenLastCalledWith(7, ""));
 await user.selectOptions(screen.getByLabelText("Project", { exact: true }), "p");
 await waitFor(() => expect(request).toHaveBeenLastCalledWith(7, "p"));
 await screen.findByRole("heading", { name: "Delivery outcomes" });
 await user.click(screen.getByRole("button", { name: "Refresh" }));
 await waitFor(() => expect(request).toHaveBeenCalledTimes(4));
});

it("preserves controlled URL filters and emits SPA drilldowns with exact completion boundaries", async () => {
 vi.spyOn(analyticsClient, "summary").mockResolvedValue(data);
 const navigate = vi.fn(); const changes = vi.fn();
 function Page() { const [filters, setFilters] = useState<AnalyticsFilters>({ days: 30, projectId: "p" }); return <AnalyticsPage overview={overview} filters={filters} onFilters={value => { changes(value); setFilters(value); }} onNavigate={navigate} />; }
 const user = userEvent.setup(); render(<Page />);
 await user.click(await screen.findByRole("link", { name: "View retained deployments" }));
 expect(navigate).toHaveBeenLastCalledWith({ view: "deployments", deploymentFilters: { layout: "list", project: "p", status: undefined, completedFrom: data.period!.start, completedTo: data.period!.end } });
 const firstDay = screen.getByRole("button", { name: /Aug 20: 4 succeeded, 1 failed, 1 cancelled/ });
 fireEvent.keyDown(firstDay, { key: "Enter" });
 expect(navigate.mock.calls.at(-1)![0].deploymentFilters).toEqual(expect.objectContaining({ completedFrom: "2026-08-20T12:00:00Z", completedTo: "2026-08-21T00:00:00.000Z" }));
 await user.click(screen.getByRole("link", { name: "Inspect last failure" }));
 expect(navigate).toHaveBeenLastCalledWith({ view: "deployments", deploymentID: "failed-run" });
 await user.click(screen.getByRole("tab", { name: "Data" }));
 expect(changes).toHaveBeenLastCalledWith({ days: 30, projectId: "p", section: "data" });
 expect(routePath({ view: "analytics", analyticsFilters: changes.mock.calls.at(-1)![0] })).toContain("section=data");
});

it("keeps missing duration samples as gaps and never turns an empty prior period into a percentage gain", async () => {
 vi.spyOn(analyticsClient, "summary").mockResolvedValue({ ...data, previousTotals: { ...data.totals, deployments: zero } });
 render(<AnalyticsPage overview={overview} />);
 await screen.findByText("No previous-period baseline");
 expect(screen.queryByText(/Infinity|NaN/)).toBeNull();
 expect(screen.getAllByText("No comparable previous sample")).toHaveLength(3);
 const chart = screen.getByRole("group", { name: /Estimated median and p95 duration/ });
 expect(chart.querySelectorAll(".analytics-duration-median polyline")).toHaveLength(2);
});

it("shows cancelled-only periods without a fabricated success rate or duration", async () => {
 const cancelled = { ...zero, runs: 4, cancelled: 4 };
 vi.spyOn(analyticsClient, "summary").mockResolvedValue({ ...data, totals: { ...totals, deployments: cancelled }, daily: [{ ...daily[0], deployments: cancelled }], failureHotspots: [], slowWorkloads: [] });
 render(<AnalyticsPage overview={overview} />);
 await screen.findByText("No failed deployments recorded in this period.");
 expect(within(screen.getByLabelText("deployments summary")).getAllByText("—")).toHaveLength(3);
 expect(screen.getByText("No successful runs with recorded duration in this period.")).toBeTruthy();
});

it("keeps unavailable snapshots useful and suppresses comparisons while backfill is incomplete", async () => {
 const request = vi.spyOn(analyticsClient, "summary").mockResolvedValue({ ...data, state: "unavailable" });
 const user = userEvent.setup(); render(<AnalyticsPage overview={overview} />);
 await screen.findByText(/Updates paused; showing the last available summary/);
 expect(screen.getByRole("heading", { name: "Delivery outcomes" })).toBeTruthy();
 request.mockResolvedValue({ ...data, state: "catching_up" });
 await user.click(screen.getByRole("button", { name: "Refresh" }));
 await screen.findByText(/period comparisons are paused/);
 expect(screen.queryByText(/13.3 pp higher/)).toBeNull();
});

it("handles loading, disabled, preparing, empty, and request failures without stale metrics", async () => {
 let resolve!: (value: AnalyticsDashboard) => void;
 const request = vi.spyOn(analyticsClient, "summary").mockImplementationOnce(() => new Promise(value => { resolve = value; }));
 const user = userEvent.setup(); render(<AnalyticsPage overview={overview} />);
 expect(screen.getByText("Loading analytics…")).toBeTruthy();
 resolve({ ...data, state: "disabled", updatedAt: undefined });
 await screen.findByText("Historical analytics is disabled");
 request.mockResolvedValue({ ...data, state: "starting", updatedAt: undefined });
 await user.click(screen.getByRole("button", { name: "Refresh" }));
 await screen.findByText("Preparing historical data");
 request.mockResolvedValue({ ...data, totals: { ...totals, deployments: zero } });
 await user.click(screen.getByRole("button", { name: "Refresh" }));
 await screen.findByText("No completed deployments in this period");
 request.mockRejectedValue(new Error("Permission denied"));
 await user.click(screen.getByRole("button", { name: "Refresh" }));
 await screen.findByRole("alert");
 expect(screen.getByText("Permission denied")).toBeTruthy();
 expect(screen.queryByLabelText("deployments summary")).toBeNull();
});

it("formats duration boundaries without displaying sixty-second or sixty-minute remainders", () => {
 expect(formatDuration(null)).toBe("—");
 expect(formatDuration(0.01)).toBe("10ms");
 expect(formatDuration(59.8)).toBe("1m");
 expect(formatDuration(7199)).toBe("2h");
});

it("preserves nanosecond period boundaries in day drilldowns and supports keyboard chart and tab inspection", async () => {
 const period = { start: "2026-08-20T12:00:00.000000123Z", end: "2026-09-19T12:00:00.000000789Z" };
 vi.spyOn(analyticsClient, "summary").mockResolvedValue({ ...data, period });
 const navigate = vi.fn(); render(<AnalyticsPage overview={overview} onNavigate={navigate} />);
 const first = await screen.findByRole("button", { name: /Aug 20: 4 succeeded/ });
 fireEvent.keyDown(first, { key: "Enter" });
 expect(navigate.mock.calls.at(-1)![0].deploymentFilters.completedFrom).toBe(period.start);
 fireEvent.keyDown(screen.getByRole("button", { name: /Sep 19: 4 succeeded/ }), { key: "Enter" });
 expect(navigate.mock.calls.at(-1)![0].deploymentFilters.completedTo).toBe(period.end);
 const chart = screen.getByRole("group", { name: /Estimated median and p95 duration/ });
 const point = within(chart).getByRole("img", { name: /Aug 20: median/ });
 fireEvent.focus(point); fireEvent.keyDown(point, { key: "ArrowRight" });
 expect(chart.closest("figure")!.querySelector("figcaption")!.textContent).toContain("0 samples");
 fireEvent.keyDown(screen.getByRole("tab", { name: "Trends & insights" }), { key: "End" });
 expect(screen.getByRole("tab", { name: "Data" }).getAttribute("aria-selected")).toBe("true");
 expect(document.activeElement).toBe(screen.getByRole("tab", { name: "Data" }));
});
