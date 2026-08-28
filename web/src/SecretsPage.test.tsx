// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { SecretsPage } from "./App";
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

afterEach(cleanup);

describe("secret actions", () => {
  it("uses icon controls with visible tooltip labels", () => {
    render(<SecretsPage overview={overview} onChanged={async () => undefined} onDelete={() => undefined} />);

    const publicKey = screen.getByRole("button", { name: "View public key for Relay key" });
    const edit = screen.getByRole("button", { name: "Edit Relay key" });
    const remove = screen.getByRole("button", { name: "Delete Relay key" });

    expect(publicKey.textContent).toBe("");
    expect(edit.textContent).toBe("");
    expect(remove.textContent).toBe("");
    expect(publicKey.getAttribute("data-tooltip")).toBe("Public key");
    expect(edit.getAttribute("data-tooltip")).toBe("Edit");
    expect(remove.getAttribute("data-tooltip")).toBe("Delete");
  });

  it("switches to an external secret reference without showing a value field", () => {
	const withStore: Overview = { ...overview, secretStores: [{
	  id: "store-1", name: "Production secrets", provider: "ibm_cloud_secrets_manager", config: { serviceUrl: "https://example.secrets-manager.appdomain.cloud" },
	  credentialsConfigured: true, state: "ready", createdAt: "2026-08-19T12:00:00Z", updatedAt: "2026-08-19T12:00:00Z",
	}] };
	render(<SecretsPage overview={withStore} onChanged={async () => undefined} onDelete={() => undefined} />);
	fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
	fireEvent.click(screen.getByRole("radio", { name: /External store/ }));

	expect(screen.getByLabelText("Secret ID")).toBeTruthy();
	expect(screen.queryByLabelText("Secret value")).toBeNull();
  });
});
