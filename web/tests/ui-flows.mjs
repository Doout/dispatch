import assert from "node:assert/strict";
import { after, before, test } from "node:test";
import { chromium } from "playwright";
import { preview } from "vite";

let browser, server, origin;
before(async () => {
  server = await preview({ preview: { host: "127.0.0.1", port: 0 } });
  origin = server.resolvedUrls.local[0];
  browser = await chromium.launch();
});
after(async () => {
  await browser?.close();
  await server?.close();
});

const settings = { operationsEnabled: false, uiFeatures: {} };
const overview = {
  identity: { id: "owner", username: "owner", systemRole: "owner", permissions: ["connections.manage"] },
  controllerSettings: settings, secretStorageConfigured: true, projectPermissions: {},
  projects: [], apps: [], servers: [], deployments: [], eventTriggers: [], previews: [],
  previewGroups: [], previewGroupRuns: [], secrets: [], secretStores: [], githubApps: [], relayWebhooks: [],
};

async function openPage(t, width, fixtureOverrides = {}) {
  const page = await browser.newPage({ viewport: { width, height: 900 } });
  t.after(() => page.close());
  page.setDefaultTimeout(7000);
  const errors = [];
  page.on("pageerror", error => errors.push(error.message));
  t.after(() => assert.deepEqual(errors, [], "Browser errors or unexpected requests"));
  let analyticsAttempts = 0;
  const fixtures = {
    hosted: { mode: "tenant", backendMode: "connected", origin, loginUrl: new URL("/login", origin).href, authentication: "doodship",
      tenant: { id: "tenant", name: "Test", slug: "test", state: "ready" },
      connection: { state: "connected", remoteAccess: true, instanceUrl: "https://controller.example.test", backendMode: "connected" } },
    analytics: { state: "disabled" },
    "repository-connections": [],
    "events/rules": [], "events/activity": { items: [], total: 0 }, "secrets/usage": [],
    overview: structuredClone(overview), settings, "deployment-catalog": { items: [] },
    ...fixtureOverrides,
  };
  await page.route("**/api/v1/**", route => {
    const key = new URL(route.request().url()).pathname.slice("/api/v1/".length);
    if (key === "projects" && route.request().method() === "POST") {
      const project = { ...route.request().postDataJSON(), id: "project", createdAt: "2026-01-01T00:00:00Z" };
      fixtures.overview.projects.push(project);
      return route.fulfill({ status: 201, json: project });
    }
    if (route.request().method() === "GET" && Object.hasOwn(fixtures, key)) {
      if (key === "analytics" && analyticsAttempts++ === 0) {
        return route.fulfill({ status: 503, json: { detail: "Temporarily unavailable" } });
      }
      return route.fulfill({ json: fixtures[key] });
    }
    errors.push(`Unexpected request: ${route.request().method()} ${key}`);
    return route.abort();
  });
  await page.routeWebSocket("**/*", socket => socket.close());
  return page;
}

for (const [width, sideBySide] of [[390, false], [1000, false], [1440, true], [1720, true]]) {
  test(`GitHub App form at ${width}px`, async t => {
    const page = await openPage(t, width);
    await page.goto(new URL("/connections", origin).href);
    await page.getByRole("button", { name: /GitHub App/ }).click();
    const editor = page.getByRole("region", { name: "Add GitHub App" });
    await editor.getByLabel("Outbound proxy").selectOption("app");
    await editor.getByLabel("Proxy URL").fill("https://proxy.example.test:3129");
    await editor.getByText("GitHub endpoints").click();
    await editor.getByLabel("GitHub URL", { exact: true }).fill("https://github.example.test");

    const details = await editor.getByRole("region", { name: "App details" }).boundingBox();
    const network = await editor.getByRole("region", { name: "Network access" }).boundingBox();
    if (sideBySide) {
      assert.equal(details.y, network.y);
      assert.ok(network.x >= details.x + details.width - 1);
    } else {
      assert.ok(network.y >= details.y + details.height - 1);
    }
    const widths = await editor.evaluate(el => {
      const page = el.parentElement;
      const style = getComputedStyle(page);
      return {
        available: page.clientWidth - parseFloat(style.paddingLeft) - parseFloat(style.paddingRight),
        editor: el.getBoundingClientRect().width,
        overflow: el.scrollWidth - el.clientWidth,
        pageOverflow: document.documentElement.scrollWidth - innerWidth,
      };
    });
    assert.ok(Math.abs(widths.editor - widths.available) <= 1, "Editor must fill the settings page");
    assert.ok(widths.overflow <= 1 && widths.pageOverflow <= 1, "Controls must fit without horizontal scrolling");
  });
}

for (const width of [390, 1440]) {
  test(`Shared navigation and forms at ${width}px`, async t => {
    const page = await openPage(t, width);
    await page.goto(new URL("/connections", origin).href);
    async function navigate(name) {
      if (width === 390) await page.getByRole("button", { name: "Open navigation" }).click();
      const link = page.getByRole("complementary", { name: "Primary navigation" }).getByRole("link", { name, exact: true });
      const href = await link.getAttribute("href");
      await link.click();
      await page.waitForURL(new URL(href, origin).href);
    }
    await navigate("Workloads");
    await page.getByRole("heading", { name: "Applications", level: 1 }).waitFor();
    await navigate("Infrastructure");
    await page.getByRole("heading", { name: "Servers", level: 1 }).waitFor();
    await navigate("Analytics");
    await page.getByRole("alert").getByText("Analytics could not be loaded").waitFor();
    await page.getByRole("button", { name: "Try again" }).click();
    await page.getByText("Historical analytics is disabled").waitFor();
    await navigate("Automation");
    await page.getByRole("navigation", { name: "Automation pages" }).waitFor();
    await navigate("Projects");
    await page.getByRole("button", { name: "Add project" }).first().click();
    const dialog = page.getByRole("dialog", { name: "Add project" });
    const save = dialog.getByRole("button", { name: "Add project" });
    assert.ok(await save.isDisabled(), "An empty project name must block submission");
    await dialog.getByLabel("Project name").fill("Browser fixture");
    await save.click();
    await page.getByText("Browser fixture", { exact: true }).waitFor();
    await page.reload();
    await page.getByText("Browser fixture", { exact: true }).waitFor();

    await navigate("Settings");
    await page.getByRole("link", { name: "Variables", exact: true }).click();
    await page.getByRole("button", { name: "Add value" }).first().click();
    await page.getByRole("radio", { name: /Plain value/ }).check();
    await page.getByRole("combobox", { name: /^Type\b/ }).selectOption("json");
    await page.getByLabel("Name", { exact: true }).fill("Test variable");
    await page.getByLabel("Environment variable", { exact: true }).fill("CONFIG");
    await page.getByLabel("Value", { exact: true }).fill("[]");
    await page.getByRole("button", { name: "Add variable" }).click();
    await page.getByRole("alert").filter({ hasText: "Enter a non-empty JSON object." }).waitFor();
    await page.getByRole("button", { name: "Cancel", exact: true }).first().click();
    await page.getByLabel("Environment variable", { exact: true }).waitFor({ state: "hidden" });
    await page.getByRole("link", { name: "Connections", exact: true }).click();
    await page.goBack();
    await page.getByRole("heading", { name: "Variables", level: 1 }).waitFor();
    await page.goForward();
    await page.getByRole("heading", { name: "Connections", level: 1 }).waitFor();
  });
}

test("A project can switch a named repository connection from a PAT to an installed App", async t => {
  const app = { id: "app", name: "Enterprise app", appId: 1, installationId: 2, webUrl: "https://github.example.test", state: "ready" };
  const token = { id: "token", name: "Temporary access", type: "github_token" };
  const page = await openPage(t, 390, {
    overview: { ...overview, projects: [{ id: "project", name: "Example" }], githubApps: [app], secrets: [token] },
    "github-apps/app/installations": [],
  });
  let saved;
  const requests = [];
  await page.route("**/api/v1/repository-connections{,/*}", route => {
    const request = route.request();
    if (request.method() === "GET") return route.fulfill({ json: saved ? [saved] : [] });
    requests.push({ method: request.method(), path: new URL(request.url()).pathname, body: request.postDataJSON() });
    saved = { ...request.postDataJSON(), id: "repository", updatedAt: "2026-01-01T00:00:00Z" };
    return route.fulfill({ json: saved });
  });

  await page.goto(new URL("/connections", origin).href);
  await page.getByRole("heading", { name: "Repository connections" }).waitFor();
  await page.getByRole("button", { name: "Add connection" }).click();
  await page.getByRole("menuitem", { name: /Repository connection/ }).click();
  const create = page.getByRole("region", { name: "Add repository connection" });
  await create.getByRole("textbox", { name: /^Connection name/ }).fill("gitops-read");
  await create.getByRole("textbox", { name: /^Repository URL/ }).fill("https://github.example.test/team/service.git");
  await create.getByRole("combobox", { name: /^Token variable/ }).selectOption(token.id);
  await create.getByRole("button", { name: "Add connection" }).click();
  await page.getByRole("button", { name: "Edit gitops-read" }).click();
  const edit = page.getByRole("region", { name: "Edit repository connection" });
  await edit.getByRole("combobox", { name: "Authentication" }).selectOption("app");
  await edit.getByRole("combobox", { name: /^GitHub App/ }).selectOption(app.id);
  await edit.getByRole("button", { name: "Save connection" }).click();
  await page.reload();
  await page.getByRole("region", { name: "Repository connections" }).getByText(app.name).waitFor();
  const identity = { projectId: "project", name: "gitops-read", repositoryUrl: "https://github.example.test/team/service.git" };
  assert.deepEqual(requests, [
    { method: "POST", path: "/api/v1/repository-connections", body: { ...identity, credentialSecretId: token.id } },
    { method: "PUT", path: "/api/v1/repository-connections/repository", body: { ...identity, githubAppId: app.id } },
  ]);
});

for (const width of [390, 1440]) {
  test(`Helm ownership review at ${width}px`, async t => {
    const message = "The Helm release does not belong to this Dispatch application and namespace.";
    const resource = { id: "resource", kind: "Application", name: "Order service", path: "orders.yaml", active: true };
    const revision = { id: "run", resourceId: resource.id, state: "failed", trigger: "manual", error: `stage development: Deployment failed: ${message}`, createdAt: "2026-01-01T00:00:00Z" };
    const review = { release: "orders", namespace: "development", target: "Preview cluster", revision: 12, previousAppId: "previous-workflow-application", previousProjectId: "previous-project", version: "review" };
    const page = await openPage(t, width, {
      overview: { ...overview, workflowResources: [resource], workflowRevisions: [revision] },
      "workflow/revisions": [revision],
      "workflow/resources/resource/topology": { nodes: [], edges: [], columns: [] },
      "workflow/revisions/run/stages": [{ stageName: "development", state: "failed", error: message, deploymentIds: ["deployment"] }],
      "deployments/deployment": { id: "deployment", appId: "current-workflow-application", state: "failed", message },
      "apps/current-workflow-application/helm-takeover": review,
    });
    await page.goto(new URL("/applications/resource/topology", origin).href);
    const panel = page.getByRole("region", { name: "Helm release ownership" });
    await panel.getByRole("button", { name: "Inspect release ownership" }).click();
    await panel.getByRole("heading", { name: "Take over orders?" }).waitFor();
    assert.equal(await page.getByText(revision.error, { exact: false }).count(), 0);
    assert.equal(await panel.getByText("Order service", { exact: true }).isVisible(), true);
    assert.equal(await panel.getByText(review.previousAppId).isVisible(), false);
    const confirm = panel.getByRole("button", { name: "Take over release" });
    assert.equal(await confirm.isDisabled(), true);
    await panel.getByText("Ownership details").click();
    assert.equal(await panel.getByText(review.previousAppId).isVisible(), true);
    await panel.getByRole("checkbox").check();
    assert.equal(await confirm.isEnabled(), true);
    const box = await panel.boundingBox();
    assert.ok(box.x >= 0 && box.x + box.width <= width + 1, "Review fits in the viewport");
    await panel.getByText("Ownership details").click();
    await panel.getByRole("button", { name: "Cancel" }).click();
    await panel.getByRole("button", { name: "Inspect release ownership" }).click();
    assert.equal(await confirm.isDisabled(), true);
  });
}
