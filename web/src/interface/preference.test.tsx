// @vitest-environment jsdom
import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { interfacePreferenceKey, useInterfacePreference } from "./preference";

function Preference({ identity, label = "Interface" }: { identity?: string; label?: string }) {
  const { enabled, setEnabled, saveError } = useInterfacePreference(identity);
  return <section aria-label={label}>
    <button onClick={() => setEnabled(!enabled)} aria-pressed={enabled}>{label}</button>
    {saveError && <p role="alert">{saveError}</p>}
  </section>;
}

beforeEach(() => localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

it("defaults to the current interface and cannot save an anonymous preference", async () => {
  const save = vi.spyOn(Storage.prototype, "setItem");
  const user = userEvent.setup();
  const { rerender } = render(<Preference />);
  const button = screen.getByRole("button", { name: "Interface" });
  expect(button.getAttribute("aria-pressed")).toBe("false");
  await user.click(button);
  expect(button.getAttribute("aria-pressed")).toBe("false");
  expect(save).not.toHaveBeenCalled();
  localStorage.setItem(interfacePreferenceKey("invalid-preference"), "true");
  rerender(<Preference identity="invalid-preference" />);
  expect(button.getAttribute("aria-pressed")).toBe("false");
});

it("persists choices across mounts, synchronizes consumers, and isolates accounts", async () => {
  const user = userEvent.setup();
  const { rerender, unmount } = render(<><Preference identity="first-account" label="Settings" /><Preference identity="first-account" label="Shell" /></>);
  await user.click(screen.getByRole("button", { name: "Settings" }));
  expect(screen.getByRole("button", { name: "Shell" }).getAttribute("aria-pressed")).toBe("true");
  expect(localStorage.getItem(interfacePreferenceKey("first-account"))).toBe("new");
  rerender(<Preference identity="second-account" label="Settings" />);
  expect(screen.getByRole("button", { name: "Settings" }).getAttribute("aria-pressed")).toBe("false");
  expect(localStorage.getItem(interfacePreferenceKey("second-account"))).toBeNull();
  unmount();
  render(<Preference identity="first-account" />);
  expect(screen.getByRole("button", { name: "Interface" }).getAttribute("aria-pressed")).toBe("true");
  await user.click(screen.getByRole("button", { name: "Interface" }));
  expect(localStorage.getItem(interfacePreferenceKey("first-account"))).toBe("current");
});

it("responds to changes and clearing preferences from another tab", () => {
  const key = interfacePreferenceKey("cross-tab-account");
  render(<Preference identity="cross-tab-account" />);
  const button = screen.getByRole("button", { name: "Interface" });
  act(() => {
    localStorage.setItem(key, "new");
    window.dispatchEvent(new StorageEvent("storage", { key, newValue: "new" }));
  });
  expect(button.getAttribute("aria-pressed")).toBe("true");
  act(() => {
    localStorage.clear();
    window.dispatchEvent(new StorageEvent("storage", { key: null }));
  });
  expect(button.getAttribute("aria-pressed")).toBe("false");
});

it("keeps a failed save usable for the tab, warns about persistence, and recovers on retry", async () => {
  const save = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("Storage unavailable"); });
  const user = userEvent.setup();
  const { rerender } = render(<Preference identity="restricted-storage-account" />);
  await user.click(screen.getByRole("button", { name: "Interface" }));
  expect(screen.getByRole("button", { name: "Interface" }).getAttribute("aria-pressed")).toBe("true");
  expect(screen.getByRole("alert").textContent).toContain("could not save the setting for your next visit");
  rerender(<Preference identity="unaffected-account" />);
  expect(screen.queryByRole("alert")).toBeNull();
  expect(screen.getByRole("button", { name: "Interface" }).getAttribute("aria-pressed")).toBe("false");
  rerender(<Preference identity="restricted-storage-account" />);
  expect(screen.getByRole("button", { name: "Interface" }).getAttribute("aria-pressed")).toBe("true");
  save.mockRestore();
  await user.click(screen.getByRole("button", { name: "Interface" }));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(localStorage.getItem(interfacePreferenceKey("restricted-storage-account"))).toBe("current");
});

it("can render when browser storage cannot be read", () => {
  vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("Storage unavailable"); });
  render(<Preference identity="no-storage-account" />);
  expect(screen.getByRole("button", { name: "Interface" }).getAttribute("aria-pressed")).toBe("false");
});
