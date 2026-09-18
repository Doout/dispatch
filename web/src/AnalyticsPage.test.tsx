// @vitest-environment jsdom
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { AnalyticsPage } from "./AnalyticsPage";
import { api, type AnalyticsCounts, type AnalyticsSummary } from "./api";

const zero: AnalyticsCounts = { runs: 0, succeeded: 0, failed: 0, cancelled: 0, reused: 0, durationSeconds: 0 };
const data: AnalyticsSummary = { state: "ready", days: 30, updatedAt: new Date().toISOString(), daily: [], totals: { date: "", deployments: zero, workflows: zero, jobs: zero } };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });
it("loads history once and fetches only when range or refresh changes", async () => {
 const request = vi.spyOn(api, "analytics").mockResolvedValue(data);
 const user = userEvent.setup();
 render(<AnalyticsPage />);
 await screen.findByText("No completed runs in this period.");
 expect(request).toHaveBeenCalledTimes(1);
 await user.selectOptions(screen.getByRole("combobox"), "7");
 await waitFor(() => expect(request).toHaveBeenLastCalledWith(7));
 await user.click(screen.getByRole("button", { name: "Refresh" }));
 await waitFor(() => expect(request).toHaveBeenCalledTimes(3));
});
it("renders a preparing state without blocking the page", async () => {
 vi.spyOn(api, "analytics").mockResolvedValue({ ...data, updatedAt: undefined, state: "starting" });
 render(<AnalyticsPage />);
 await screen.findByText("Preparing historical data. Refresh shortly.");
 expect(screen.getByRole("heading", { name: "Analytics" })).toBeTruthy();
});
it("keeps export failures separate from page failures", async () => {
 vi.spyOn(api, "analytics").mockResolvedValue({ ...data, state: "unavailable" });
 render(<AnalyticsPage />);
 await screen.findByText(/Updates paused; showing the last available summary/);
});
