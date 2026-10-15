// @vitest-environment jsdom

import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServersPage } from "./App";
import type { Overview } from "./api";

const overview: Overview = {
  demo: false,
  secretStorageConfigured: true,
  projects: [],
  servers: [
    {
      id: "server-1",
      name: "development",
      address: "https://api.example.com:6443",
      runtime: "openshift",
      state: "ready",
      agentMode: "",
      createdAt: "2026-08-19T12:00:00Z",
    },
  ],
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
  it("separates builders from deployment targets", () => {
    const withBuilder = {
      ...overview,
      servers: [
        ...overview.servers,
        {
          id: "builder-1",
          name: "build-host",
          address: "ssh://build@example.com",
          runtime: "builder" as const,
          state: "ready",
          agentMode: "ssh",
          builder: {
            sshSecretId: "key",
            hostKey: "ssh-ed25519 AAAA",
            maxConcurrent: 2,
          },
          createdAt: "2026-08-19T12:00:00Z",
        },
      ],
    };
    render(
      <ServersPage
        overview={withBuilder}
        onChanged={async () => undefined}
        onAdd={() => undefined}
        onRepair={() => undefined}
        onEdit={() => undefined}
        onDelete={() => undefined}
      />,
    );
    expect(screen.getByText("Docker builders")).not.toBeNull();
    expect(screen.getByText("build-host")).not.toBeNull();
    expect(screen.getByText("2 jobs")).not.toBeNull();
    expect(
      screen.getByRole("button", { name: "Edit build-host" }),
    ).not.toBeNull();
  });
  it("lists local Docker as both a target and a managed builder", () => {
    const local = {
      id: "local",
      name: "local-docker",
      address: "local",
      runtime: "docker" as const,
      state: "ready",
      agentMode: "local",
      createdAt: "2026-08-19T12:00:00Z",
    };
    render(
      <ServersPage
        overview={{ ...overview, servers: [local] }}
        onChanged={async () => undefined}
        onAdd={() => undefined}
        onRepair={() => undefined}
        onEdit={() => undefined}
        onDelete={() => undefined}
      />,
    );
    expect(screen.getAllByText("local-docker")).toHaveLength(2);
    expect(screen.getAllByText("Local Docker socket")).toHaveLength(2);
    const builders = screen
      .getByRole("heading", { name: "Docker builders" })
      .closest("section")!;
    expect(within(builders).getByText("1 job")).not.toBeNull();
    expect(within(builders).getByText("ready")).not.toBeNull();
    expect(within(builders).getByText("Managed")).not.toBeNull();
    expect(
      screen.queryByRole("button", { name: "Edit local-docker" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete local-docker" }),
    ).toBeNull();
    expect(screen.queryByText("No Docker builders.")).toBeNull();
  });

  it("shows local socket unavailability in the builder pool", () => {
    const local = {
      id: "local",
      name: "local-docker",
      address: "local",
      runtime: "docker" as const,
      state: "unavailable",
      agentMode: "local",
      createdAt: "2026-08-19T12:00:00Z",
    };
    render(
      <ServersPage
        overview={{ ...overview, servers: [local] }}
        canManage={false}
        onChanged={async () => undefined}
        onAdd={() => undefined}
        onRepair={() => undefined}
        onEdit={() => undefined}
        onDelete={() => undefined}
      />,
    );
    const builders = screen
      .getByRole("heading", { name: "Docker builders" })
      .closest("section")!;
    expect(within(builders).getByText("unavailable")).not.toBeNull();
  });

  it("uses the shared icon controls and tooltip labels", () => {
    const onRepair = vi.fn();
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    render(
      <ServersPage
        overview={overview}
        onChanged={async () => undefined}
        onAdd={() => undefined}
        onRepair={onRepair}
        onEdit={onEdit}
        onDelete={onDelete}
      />,
    );

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
    render(
      <ServersPage
        overview={overview}
        canManage={false}
        onChanged={async () => undefined}
        onAdd={onAdd}
        onRepair={onRepair}
        onEdit={onEdit}
        onDelete={onDelete}
        onTopology={onTopology}
      />,
    );

    expect(screen.getByText("development")).not.toBeNull();
    expect(
      screen.getByRole("button", { name: "View deployments on development" }),
    ).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Add server" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Repair development" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Edit development" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete development" }),
    ).toBeNull();
  });
});
