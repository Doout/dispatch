// @vitest-environment jsdom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type Deployment } from "../api";
import { DeploymentManifests } from "./DeploymentManifests";

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

const deployment: Deployment = {
  id: "deployment-1", appId: "app-1", commitSha: "abc", specDigest: "sha256:test", state: "succeeded", message: "", createdAt: "2026-08-27T01:00:00Z",
};

describe("deployment manifests", () => {
  it("shows live YAML, its source, and instructions for adding a resource", async () => {
    vi.spyOn(api, "deploymentManifests").mockResolvedValue({
      target: "development", namespace: "store", release: "checkout",
      origin: { managed: true, repository: "platform/deployments", branch: "main", configPath: "deployment/store.yaml", configRevision: "abc1234", chartRepository: "platform/charts", chartPath: "charts/store" },
      manifests: [
        { name: "checkout-api", kind: "Deployment", apiVersion: "apps/v1", document: "apiVersion: apps/v1\nkind: Deployment\n" },
        { name: "checkout", kind: "Service", apiVersion: "v1", document: "apiVersion: v1\nkind: Service\n" },
      ],
    });

    render(<DeploymentManifests deployment={deployment} />);

    expect((await screen.findAllByText("checkout-api")).length).toBe(2);
    expect(screen.getByText("platform/deployments/deployment/store.yaml")).not.toBeNull();
    expect(screen.getByText(/kind: Deployment/)).not.toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Add resource" }));
    expect(screen.getByText("platform/charts/charts/store/templates/")).not.toBeNull();

    await userEvent.type(screen.getByLabelText("Filter manifests"), "service");
    expect(screen.queryByText("checkout-api")).toBeNull();
    expect(screen.getByRole("button", { name: "View Service checkout" })).not.toBeNull();
  });
});
