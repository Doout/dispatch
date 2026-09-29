// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { api, type EventActivity, type EventRule } from "./api";
import { EventsListPage } from "./EventsPage";
const now = new Date().toISOString();
const check: EventActivity = {
  id: "check",
  projectId: "project",
  ruleId: "repo",
  name: "Repository",
  transport: "poll",
  kind: "preview_check",
  repository: "team/ui",
  state: "failed",
  message: "GitHub returned 503",
  createdAt: now,
  check: true,
};
const rule: EventRule = {
  id: "template:1",
  name: "Agent previews",
  kind: "template",
  repositories: ["team/service", "team/ui"],
  command: "/preview",
  mode: "poll",
  intervalSeconds: 30,
  enabled: true,
  check,
};
const event: EventActivity = {
  ...check,
  id: "event",
  ruleId: rule.id,
  name: rule.name,
  state: "processed",
  message: undefined,
  check: false,
  kind: "pull_request_comment",
  pullRequest: 17,
  resourceId: "preview",
  revisionIds: ["run-1"],
  previewUrl: "https://preview.example.test/17",
};
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});
function page(
  section: "rules" | "activity",
  open = vi.fn().mockResolvedValue(undefined),
) {
  render(
    <EventsListPage
      section={section}
      onSectionChange={vi.fn()}
      onConfigure={vi.fn()}
      canConfigure
      onEditHooks={vi.fn()}
      onOpenRun={open}
    />,
  );
  return open;
}
it("shows polling templates, watched repositories, and failed check details", async () => {
  vi.spyOn(api, "eventRules").mockResolvedValue([rule]);
  vi.spyOn(api, "eventActivity").mockResolvedValue({
    items: [event],
    total: 1,
  });
  page("rules");
  expect(await screen.findByText("Agent previews")).toBeTruthy();
  expect(screen.getByText("team/service")).toBeTruthy();
  expect(screen.getByText("team/ui")).toBeTruthy();
  expect(screen.getByText("Every 30 seconds")).toBeTruthy();
  expect(screen.getByText("Check failed")).toBeTruthy();
  expect(screen.getByText("GitHub returned 503")).toBeTruthy();
  expect(screen.queryByText("No event rules")).toBeNull();
});
it("filters and pages deliveries and opens the selected run", async () => {
  vi.spyOn(api, "eventRules").mockResolvedValue([rule]);
  const request = vi
    .spyOn(api, "eventActivity")
    .mockResolvedValueOnce({ items: [event], total: 2, next: "cursor" })
    .mockResolvedValueOnce({
      items: [{ ...event, id: "older", transport: "webhook", revisionIds: [] }],
      total: 2,
    })
    .mockResolvedValueOnce({ items: [event], total: 1 });
  const open = page("activity");
  const button = await screen.findByRole("button", { name: "View run run-1" });
  fireEvent.click(button);
  expect(open).toHaveBeenCalledWith("run-1");
  expect(
    screen.getByRole("link", { name: "Open preview" }).getAttribute("href"),
  ).toBe(event.previewUrl);
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  await screen.findByText("Webhook");
  expect(request).toHaveBeenCalledWith("", "cursor");
  fireEvent.change(screen.getByLabelText("Delivery"), {
    target: { value: "poll" },
  });
  await screen.findByRole("button", { name: "View run run-1" });
  expect(request).toHaveBeenCalledWith("poll");
});
it("describes detected source changes without calling them scans", async () => {
  vi.spyOn(api, "eventRules").mockResolvedValue([rule]);
  vi.spyOn(api, "eventActivity").mockResolvedValue({
    items: [{ ...event, kind: "branch_scan" }],
    total: 1,
  });
  page("activity");
  expect(await screen.findByText("Source changed")).toBeTruthy();
  expect(screen.queryByText("branch scan")).toBeNull();
  expect(screen.getByRole("button", { name: "View run run-1" })).toBeTruthy();
});
it("shows load failures without claiming zero rules or events, then retries", async () => {
  vi.spyOn(api, "eventRules")
    .mockRejectedValueOnce(new Error("Access unavailable"))
    .mockResolvedValue([rule]);
  vi.spyOn(api, "eventActivity")
    .mockRejectedValueOnce(new Error("Unavailable"))
    .mockResolvedValue({ items: [], total: 0 });
  page("rules");
  expect(await screen.findByRole("alert")).toHaveProperty(
    "textContent",
    "Could not load event rules: Access unavailable",
  );
  expect(screen.queryByText("No event rules")).toBeNull();
  expect(screen.queryByText("0")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(await screen.findByText("Agent previews")).toBeTruthy();
});
it("discards an older page response after the delivery filter changes", async () => {
  vi.spyOn(api, "eventRules").mockResolvedValue([rule]);
  let finish: (page: import("./api").EventActivityPage) => void = () =>
    undefined;
  vi.spyOn(api, "eventActivity")
    .mockResolvedValueOnce({ items: [event], total: 2, next: "cursor" })
    .mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    )
    .mockResolvedValueOnce({ items: [event], total: 1 });
  page("activity");
  await screen.findByRole("button", { name: "Load more" });
  fireEvent.click(screen.getByRole("button", { name: "Load more" }));
  fireEvent.change(screen.getByLabelText("Delivery"), {
    target: { value: "poll" },
  });
  await screen.findByRole("button", { name: "View run run-1" });
  await import("@testing-library/react").then(({ act }) =>
    act(async () => {
      finish({
        items: [
          { ...event, id: "stale", name: "Wrong filter", transport: "webhook" },
        ],
        total: 2,
      });
    }),
  );
  expect(screen.queryByText("Wrong filter")).toBeNull();
  expect(screen.queryByText("Webhook")).toBeNull();
});
