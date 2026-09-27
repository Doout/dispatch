// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectionsPage } from "./ConnectionsPage";
import { api, type Overview, type PrivateNetwork } from "./api";

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

beforeEach(() => {
  vi.spyOn(api, "githubAppInstallations").mockResolvedValue([]);
  vi.spyOn(api, "githubAppRepositories").mockResolvedValue([]);
});

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

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
        slug: "dispatch-example",
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
      .toBe("https://github.example.com/github-apps/dispatch-example/installations/new");
    expect(installLink.getAttribute("target")).toBeNull();
  });

  it("shows missing App and installation access after verification", async () => {
    const connection: Overview["githubApps"][number] = {
      id: "app-1", name: "Dispatch", webUrl: "https://github.example.com", apiUrl: "https://github.example.com/api/v3",
      appId: 42, slug: "dispatch-example", installationId: 73, installationUrl: "https://github.example.com/settings/installations/73",
      privateKeyConfigured: true, webhookSecretConfigured: true, state: "ready",
      createdAt: "2026-08-27T00:00:00Z", updatedAt: "2026-08-27T00:00:00Z",
    };
    vi.spyOn(api, "githubAppInstallations").mockResolvedValue([]);
    vi.spyOn(api, "verifyGitHubApp").mockResolvedValue({ connection, verification: {
      slug: "dispatch-example", clientId: "", registrationOwner: "", registrationOwnerType: "User",
      installationAccount: "Example", repositorySelection: "selected", repositoryCount: 1, pushSubscribed: false,
      appPermissionsAvailable: true, installationPermissionsAvailable: true,
      tokenPermissionsAvailable: true, missingTokenPermissions: [],
      missingAppPermissions: [{ name: "pull_requests", required: "write", granted: "read" }],
      missingInstallationPermissions: [{ name: "pull_requests", required: "write", granted: "read" }],
    } });
    render(<ConnectionsPage overview={{ ...overview, githubApps: [connection] }} selectedConnectionID="app-1" notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    await waitFor(() => expect((screen.getByRole("button", { name: "Verify permissions" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "Verify permissions" }));

    expect(await screen.findByText("App registration needs write for pull_requests; GitHub grants read.")).toBeTruthy();
    expect(screen.getByText("Installation needs write for pull_requests; GitHub grants read.")).toBeTruthy();
    expect(screen.getByRole("link", { name: /Change App permissions/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Approve account access/ })).toBeTruthy();
  });
});


it("labels legacy edge credentials and reviews rotation before invalidating the node", async () => {
 const network: PrivateNetwork = { id: "node", name: "Private node", driver: "dispatch_agent", config: {}, details: {}, state: "ready", createdAt: "2026-09-20T00:00:00Z", updatedAt: "2026-09-20T00:00:00Z" };
 const rotate = vi.spyOn(api,"rotatePrivateNetworkToken").mockResolvedValue({...network,enrollmentToken:"one-time-token",details:{credentialMode:"short_session"}});
 render(<ConnectionsPage overview={{...overview,privateNetworks:[network]}} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);
 expect(screen.getByText("Legacy token · re-enroll to upgrade")).toBeTruthy();
 fireEvent.click(screen.getByRole("button",{name:"Create install token for Private node"}));
 expect(screen.getByRole("group",{name:"Confirm node credential change"})).toBeTruthy();expect(rotate).not.toHaveBeenCalled();
 fireEvent.click(screen.getByRole("button",{name:"Create enrollment token"}));
 await screen.findByText(/The enrollment token expires in 15 minutes/);expect(rotate).toHaveBeenCalledWith("node");
});

it("shows both organization installations without replacing the current connection", async () => {
 const connection: Overview["githubApps"][number] = {id: "app", name: "Dispatch", webUrl: "https://github.example.com", apiUrl: "https://github.example.com/api/v3", appId: 42, installationId: 73, slug: "dispatch", privateKeyConfigured: true, webhookSecretConfigured: true, state: "ready", createdAt: "", updatedAt: ""};
 vi.spyOn(api, "githubAppInstallations").mockResolvedValue([
  {id: 73, account: "Alpha", target: "Organization", repositorySelection: "all", webUrl: "https://github.example.com/organizations/Alpha/settings/installations/73"},
  {id: 74, account: "Beta", target: "Organization", repositorySelection: "selected", webUrl: "https://github.example.com/organizations/Beta/settings/installations/74", missingPermissions: [{name: "issues", required: "write", granted: "read"}]},
 ]);
 vi.spyOn(api, "githubAppRepositories").mockResolvedValue([
  {id: 1, fullName: "Alpha/service", name: "service", owner: "Alpha", defaultBranch: "main", private: true, webUrl: "https://github.example.com/Alpha/service"},
  {id: 2, fullName: "Beta/ui", name: "ui", owner: "Beta", defaultBranch: "main", private: false, webUrl: "https://github.example.com/Beta/ui"},
 ]);
 const update = vi.spyOn(api, "updateGitHubApp");
 const open = vi.fn();
 const watched = { ...overview, githubApps: [connection], eventTriggers: [
  {id: "trigger", appId: "app", githubAppId: "app", provider: "github" as const, repository: "Alpha/service", command: "/preview", enabled: true, secretIds: [], createdAt: "", updatedAt: ""},
  {id: "missing", appId: "app", githubAppId: "app", provider: "github" as const, repository: "Gamma/docs", command: "/preview", enabled: true, secretIds: [], createdAt: "", updatedAt: ""},
 ] };
 const page = render(<ConnectionsPage overview={watched} notice="" onNotice={() => undefined} onChanged={async () => undefined} onOpenConnection={open} />);
 expect(screen.getByRole("link", {name: "View details"}).getAttribute("href")).toBe("/connections/app");
 expect(screen.queryByText("Connected accounts")).toBeNull();
 fireEvent.click(screen.getByRole("link", {name: "View details"}));
 expect(open).toHaveBeenCalledWith("app");
 page.rerender(<ConnectionsPage overview={watched} selectedConnectionID="app" notice="" onNotice={() => undefined} onChanged={async () => undefined} onOpenConnection={open} />);
 expect(await screen.findByRole("heading", {name: "Connected accounts"})).toBeTruthy();
 expect(screen.getByText(/All repositories · 1 accessible/)).toBeTruthy();
 expect(screen.getByText(/Selected repositories · 1 accessible/)).toBeTruthy();
 expect(screen.getByRole("link", {name: "Manage Alpha access"})).toBeTruthy();
 expect(screen.getByRole("link", {name: "Manage Beta access"})).toBeTruthy();
 expect(screen.getByRole("alert").textContent).toContain("Missing permissions");
 expect(screen.getAllByText("Application preview: /preview")).toHaveLength(2);
 expect(screen.getByText("App access confirmed")).toBeTruthy();
 expect(screen.getByText("No App access")).toBeTruthy();
 expect(update).not.toHaveBeenCalled();
});
