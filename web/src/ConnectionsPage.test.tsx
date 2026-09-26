// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
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

beforeEach(() => { vi.spyOn(api, "githubAppInstallations").mockResolvedValue([]); });

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
    render(<ConnectionsPage overview={{ ...overview, githubApps: [connection] }} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);

    fireEvent.click(screen.getByRole("button", { name: "Verify" }));

    expect(await screen.findByText("App registration needs these repository permissions:")).toBeTruthy();
    expect(screen.getByText("The installation still needs these repository permissions approved:")).toBeTruthy();
    expect(screen.getAllByText("Pull requests: write (currently read)")).toHaveLength(2);
    expect(screen.getByRole("link", { name: /Edit App permissions/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Approve installation access/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Install or reinstall App/ })).toBeTruthy();
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
 const update = vi.spyOn(api, "updateGitHubApp");
 render(<ConnectionsPage overview={{...overview, githubApps: [connection]}} notice="" onNotice={() => undefined} onChanged={async () => undefined} />);
 fireEvent.click(screen.getByRole("button", {name: "Refresh installations"}));
 expect(await screen.findByText("2 connected")).toBeTruthy();
 expect(screen.getByRole("link", {name: "Manage Alpha access"})).toBeTruthy();
 expect(screen.getByRole("link", {name: "Manage Beta access"})).toBeTruthy();
 expect(screen.getByRole("alert").textContent).toContain("Missing permissions");
 expect(screen.queryByText("Choose an installation")).toBeNull();
 expect(update).not.toHaveBeenCalled();
});
