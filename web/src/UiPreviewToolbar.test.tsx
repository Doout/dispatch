// @vitest-environment jsdom
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";
import { UiPreviewToolbar, uiPreviewConfiguration } from "./UiPreviewToolbar";

const configuration = {
  source: "development" as const,
  upstream: "https://dev.example.test",
  readOnly: false,
  productionAccess: false,
};
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.getElementById("dispatch-ui-preview")?.remove();
});

it("does not enable preview controls on regular installations", () => {
  expect(uiPreviewConfiguration()).toBeNull();
});

it("asks for an access key before switching to production", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  render(<UiPreviewToolbar configuration={configuration} />);
  const user = userEvent.setup();
  await user.selectOptions(screen.getByLabelText("Data source"), "production");
  expect(
    screen.getByRole("dialog", { name: "Unlock production data" }),
  ).toBeTruthy();
  expect(
    (screen.getByLabelText("Data source") as HTMLSelectElement).value,
  ).toBe("development");
  expect(fetcher).not.toHaveBeenCalled();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(fetcher).not.toHaveBeenCalled();
});

it("keeps an incorrect key out of source selection and browser storage", async () => {
  const fetcher = vi.fn(
    async (_path: string, _options?: RequestInit) =>
      new Response(
        JSON.stringify({ detail: "The preview access key is incorrect." }),
        { status: 403 },
      ),
  );
  vi.stubGlobal("fetch", fetcher);
  render(<UiPreviewToolbar configuration={configuration} />);
  const user = userEvent.setup();
  await user.selectOptions(screen.getByLabelText("Data source"), "production");
  await user.type(screen.getByLabelText("Preview access key"), "wrong-key");
  await user.click(screen.getByRole("button", { name: "Unlock production" }));
  await screen.findByRole("alert");
  expect(fetcher).toHaveBeenCalledTimes(1);
  expect(fetcher.mock.calls[0][0]).toBe("/__dispatch_ui/access");
  expect(screen.getByRole("alert").textContent).not.toContain("wrong-key");
  expect(JSON.stringify(localStorage)).not.toContain("wrong-key");
  expect(JSON.stringify(sessionStorage)).not.toContain("wrong-key");
});

it("requests a new key when loading a production page without an unlock cookie", () => {
  render(
    <UiPreviewToolbar
      configuration={{ ...configuration, source: "production", readOnly: true }}
    />,
  );
  expect(
    screen.getByRole("dialog", { name: "Unlock production data" }),
  ).toBeTruthy();
  expect(
    screen.queryByRole("button", { name: "Lock production access" }),
  ).toBeNull();
});
