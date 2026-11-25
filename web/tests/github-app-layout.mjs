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

for (const [width, sideBySide] of [[390, false], [1000, false], [1440, true], [1720, true]]) {
  test(`GitHub App form at ${width}px`, async t => {
    const page = await browser.newPage({ viewport: { width, height: 900 } });
    t.after(() => page.close());
    page.setDefaultTimeout(5000);
    const fixtures = {
      hosted: { mode: "tenant", backendMode: "connected", origin, loginUrl: new URL("/login", origin).href, authentication: "doodship",
        tenant: { id: "tenant", name: "Test", slug: "test", state: "ready" },
        connection: { state: "connected", remoteAccess: true, instanceUrl: "https://controller.example.test", backendMode: "connected" } },
      overview, settings, "deployment-catalog": { items: [] },
    };
    await page.route("**/api/v1/**", route => {
      const key = new URL(route.request().url()).pathname.slice("/api/v1/".length);
      return Object.hasOwn(fixtures, key) ? route.fulfill({ json: fixtures[key] }) : route.abort();
    });
    await page.routeWebSocket("**/*", socket => socket.close());
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
