// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectionsPage } from "./ConnectionsPage";
import type { Overview } from "./api";

const overview: Overview = {
  demo: false,
  secretStorageConfigured: true,
  projects: [],
  servers: [],
  apps: [],
  deployments: [],
  eventTriggers: [],
  previews: [],
  previewGroups: [],
  previewGroupRuns: [],
  secrets: [],
  secretStores: [],
  githubApps: [],
  relayWebhooks: [],
};

afterEach(cleanup);

describe("connections page", () => {
  it("shows one compact provider chooser without duplicate add actions", () => {
    render(<ConnectionsPage overview={overview} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    expect(screen.getByRole("heading", { name: "No connections" })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Add connection" })).toHaveLength(1);
    expect(screen.queryByRole("button", { name: "Add GitHub App" })).toBeNull();
    expect(screen.getByRole("button", { name: /GitHub App/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Secret store/ })).toBeTruthy();
  });

  it("groups connection types without binding secret stores to one provider", () => {
    render(<ConnectionsPage overview={overview} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    fireEvent.pointerDown(screen.getByRole("button", { name: "Add connection" }), { button: 0, ctrlKey: false });

    expect(screen.getAllByText("Providers")).toHaveLength(2);
    expect(screen.getAllByText("Network")).toHaveLength(2);
    expect(screen.getByRole("menuitem", { name: /External secrets/ })).toBeTruthy();
    expect(screen.queryByText("IBM Cloud Secrets Manager")).toBeNull();
  });

  it("opens secret-store setup from the provider chooser", () => {
    render(<ConnectionsPage overview={overview} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    fireEvent.click(screen.getByRole("button", { name: /Secret store/ }));

    expect(screen.getByRole("heading", { name: "Add secret store" })).toBeTruthy();
    expect(screen.getByLabelText("Service URL")).toBeTruthy();
  });

  it("opens edge-node deployment from the network group", () => {
    render(<ConnectionsPage overview={overview} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    fireEvent.click(screen.getByRole("button", { name: /Edge node/ }));

    expect(screen.getByRole("heading", { name: "Add edge node" })).toBeTruthy();
    expect(screen.getByLabelText("Name")).toBeTruthy();
    expect(screen.getByText("Outbound only")).toBeTruthy();
  });

  it("opens the Laneway network authorization flow", () => {
    render(<ConnectionsPage overview={overview} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    fireEvent.click(screen.getByRole("button", { name: /Laneway network/ }));

    expect(screen.getByRole("heading", { name: "Connect Laneway" })).toBeTruthy();
    expect(screen.getByPlaceholderText("Production network")).toBeTruthy();
    expect(screen.getByPlaceholderText("https://lane.example.com")).toBeTruthy();
    expect(screen.getByText("Scoped access")).toBeTruthy();
  });

  it("allows polling without a webhook and opens relay setup", () => {
    const onAddRelay = vi.fn();
    render(<ConnectionsPage overview={overview} notice="" onNotice={() => undefined} onChanged={async () => undefined} onAddRelay={onAddRelay} />);

    fireEvent.click(screen.getByRole("button", { name: /GitHub App/ }));

    expect((screen.getByRole("switch", { name: "Webhook events" }) as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByRole("switch", { name: "Webhook events" }));
    fireEvent.click(screen.getByRole("radio", { name: /Add a relay server/ }));
    expect(onAddRelay).toHaveBeenCalledOnce();
  });

  it("uses the GitHub Enterprise installation path", () => {
    const enterpriseOverview: Overview = {
      ...overview,
      githubApps: [{
        id: "app-1",
        name: "Dispatch",
        webUrl: "https://github.example.com",
        apiUrl: "https://github.example.com/api/v3",
        appId: 42,
        slug: "dispatch-dev",
        privateKeyConfigured: true,
        webhookSecretConfigured: true,
        state: "needs_installation",
        createdAt: "2026-08-27T00:00:00Z",
        updatedAt: "2026-08-27T00:00:00Z",
      }],
    };
    render(<ConnectionsPage overview={enterpriseOverview} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    const installLink = screen.getByRole("link", { name: /Install App/ });
    expect(installLink.getAttribute("href"))
      .toBe("https://github.example.com/github-apps/dispatch-dev/installations/new");
    expect(installLink.getAttribute("target")).toBeNull();
  });
});
