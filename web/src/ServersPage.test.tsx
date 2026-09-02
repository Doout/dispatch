// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServersPage } from "./App";
import type { Overview } from "./api";

const overview: Overview = {
  demo: false,
  secretStorageConfigured: true,
  projects: [],
  servers: [{
    id: "server-1",
    name: "development",
    address: "https://api.example.com:6443",
    runtime: "openshift",
    state: "ready",
    agentMode: "",
    createdAt: "2026-08-19T12:00:00Z",
  }],
  apps: [],
  deployments: [],
  eventTriggers: [],
  previews: [],
  previewGroups: [],
  previewGroupRuns: [],
  secrets: [],
  githubApps: [],
  relayWebhooks: [],
};

afterEach(cleanup);

describe("server actions", () => {
  it("uses the shared icon controls and tooltip labels", () => {
    const onRepair = vi.fn();
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    render(<ServersPage overview={overview} onChanged={async () => undefined} onAdd={() => undefined} onRepair={onRepair} onEdit={onEdit} onDelete={onDelete} />);

    const repair = screen.getByRole("button", { name: "Repair development" });
    const edit = screen.getByRole("button", { name: "Edit development" });
    const remove = screen.getByRole("button", { name: "Delete development" });

    expect(repair.textContent).toBe("");
    expect(edit.textContent).toBe("");
    expect(remove.textContent).toBe("");
    expect(repair.getAttribute("data-tooltip")).toBe("Repair");
    expect(edit.getAttribute("data-tooltip")).toBe("Edit");
    expect(remove.getAttribute("data-tooltip")).toBe("Delete");

    fireEvent.click(repair);
    fireEvent.click(edit);
    fireEvent.click(remove);
    expect(onRepair).toHaveBeenCalledWith(overview.servers[0]);
    expect(onEdit).toHaveBeenCalledWith(overview.servers[0]);
    expect(onDelete).toHaveBeenCalledWith(overview.servers[0]);
  });

  it("shows inventory without mutation controls for a project viewer", () => {
    const onAdd = vi.fn();
    const onRepair = vi.fn();
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    const onTopology = vi.fn();
    render(<ServersPage overview={overview} canManage={false} onChanged={async () => undefined} onAdd={onAdd} onRepair={onRepair} onEdit={onEdit} onDelete={onDelete} onTopology={onTopology} />);

    expect(screen.getByText("development")).not.toBeNull();
    expect(screen.getByRole("button", { name: "View deployments on development" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Add server" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Repair development" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit development" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete development" })).toBeNull();
  });
});
