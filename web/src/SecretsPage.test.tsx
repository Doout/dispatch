// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SecretsPage } from "./App";
import { api } from "./api";
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
  githubApps: [],
  relayWebhooks: [],
  secrets: [{
    id: "secret-1",
    name: "Relay key",
    type: "ssh_private_key",
    environmentVariable: "SSH_PRIVATE_KEY",
    publicValue: "ssh-ed25519 AAAA relay",
    createdAt: "2026-08-19T12:00:00Z",
    updatedAt: "2026-08-19T12:00:00Z",
  }],
};

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("secret actions", () => {
  it("shows the saved key with labeled actions", () => {
    render(<SecretsPage overview={overview} onChanged={async () => undefined} onDelete={() => undefined} />);

    const publicKey = screen.getByRole("button", { name: "View public key for Relay key" });
    const edit = screen.getByRole("button", { name: "Edit Relay key" });
    const remove = screen.getByRole("button", { name: "Delete Relay key" });

    expect(publicKey.textContent).toContain("Public key");
    expect(edit.textContent).toContain("Edit");
    expect(remove.textContent).toContain("Delete");
    expect(screen.getByText("Hidden after saving")).toBeTruthy();
  });

  it("switches to an external secret reference without showing a value field", () => {
	const withStore: Overview = { ...overview, secretStores: [{
	  id: "store-1", name: "Production secrets", provider: "ibm_cloud_secrets_manager", config: { serviceUrl: "https://example.secrets-manager.appdomain.cloud" },
	  credentialsConfigured: true, state: "ready", createdAt: "2026-08-19T12:00:00Z", updatedAt: "2026-08-19T12:00:00Z",
	}] };
	render(<SecretsPage overview={withStore} onChanged={async () => undefined} onDelete={() => undefined} />);
	fireEvent.click(screen.getByRole("button", { name: "Add value" }));
	fireEvent.click(screen.getByRole("radio", { name: /External store/ }));

	expect(screen.getByLabelText("Secret ID")).toBeTruthy();
	expect(screen.queryByLabelText("Secret value")).toBeNull();
  });

  it("shows plain values and allows editing them", () => {
    const plain = { ...overview.secrets[0], id: "url", name: "Development URL", type: "environment_variable" as const,
      environmentVariable: "APP_CONFIG_URL", publicValue: "https://example.test/app" };
    render(<SecretsPage overview={{ ...overview, secrets: [plain] }} onChanged={async () => undefined} onDelete={() => undefined} />);

    expect(screen.getByRole("heading", { name: "Variables" })).toBeTruthy();
    expect(screen.getByText("https://example.test/app")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "View public key for Development URL" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Edit Development URL" }));
    expect((screen.getByLabelText("Value") as HTMLInputElement).value).toBe("https://example.test/app");
  });

  it("creates a plain value without encrypted storage", async () => {
    const create = vi.spyOn(api, "createSecret").mockResolvedValue({ id: "url", name: "Development URL", type: "environment_variable", source: "local", environmentVariable: "DEV_URL", publicValue: "https://example.test", createdAt: "", updatedAt: "" });
    render(<SecretsPage overview={{ ...overview, secretStorageConfigured: false, secrets: [] }} onChanged={async () => undefined} onDelete={() => undefined} />);
    fireEvent.click(screen.getAllByRole("button", { name: "Add value" })[0]);
    expect((screen.getByRole("radio", { name: /Plain value/ }) as HTMLInputElement).checked).toBe(true);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Development URL" } });
    fireEvent.change(screen.getByLabelText("Environment variable"), { target: { value: "DEV_URL" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "https://example.test" } });
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ type: "environment_variable", source: "local", environmentVariable: "DEV_URL", value: "https://example.test" }));
  });

  it("saves JSON values and checks the object before sending it", async () => {
    const create = vi.spyOn(api, "createSecret").mockResolvedValue({ id: "bundle", name: "Development bundle", type: "environment_json", source: "local", environmentVariable: "APP_CONFIG", publicValue: '{"URL":"https://example.test"}', createdAt: "", updatedAt: "" });
    render(<SecretsPage overview={{ ...overview, secrets: [] }} onChanged={async () => undefined} onDelete={() => undefined} />);
    fireEvent.click(screen.getAllByRole("button", { name: "Add value" })[0]);
    fireEvent.click(screen.getByRole("radio", { name: /Plain value/ }));
    fireEvent.change(screen.getByLabelText("Type"), { target: { value: "json" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Development bundle" } });
    fireEvent.change(screen.getByLabelText("Environment variable"), { target: { value: "APP_CONFIG" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "[]" } });
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", "Enter a non-empty JSON object.");
    expect(create).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: '{"URL":"https://example.test"}' } });
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    expect(create).toHaveBeenCalledWith(expect.objectContaining({ type: "environment_json", environmentVariable: "APP_CONFIG", value: '{"URL":"https://example.test"}' }));
  });
});
