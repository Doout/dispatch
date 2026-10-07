// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { Overview, UIFeatureKey } from "../api";
import { InterfaceNav, InterfaceTabs } from "./Navigation";

const overview = {
  identity: { id: "owner", systemRole: "owner", permissions: [] },
  projects: [{ id: "p", name: "Project" }], projectPermissions: {},
  controllerSettings: { operationsEnabled: false },
} as unknown as Overview;
const navigate = vi.fn();
afterEach(() => { cleanup(); vi.clearAllMocks(); });

function navigation(value: Overview) {
  return <>
    <InterfaceNav overview={value} route={{ view: "settings" }} open={false} onClose={() => {}} onNavigate={navigate} />
    <InterfaceTabs overview={value} route={{ view: "infrastructure" }} onNavigate={navigate} />
    <InterfaceTabs overview={value} route={{ view: "recovery" }} onNavigate={navigate} />
    <InterfaceTabs overview={value} route={{ view: "automation" }} onNavigate={navigate} />
  </>;
}

it("keeps existing Servers and automation rules available when all experiments are off", () => {
  render(navigation(overview));
  const primary = within(screen.getByRole("complementary", { name: "Primary navigation" }));
  expect(primary.queryByRole("link", { name: "Recovery" })).toBeNull();
  expect(primary.getByRole("link", { name: "Infrastructure" }).getAttribute("href")).toBe("/servers");
  expect(primary.getByRole("link", { name: "Automation" }).getAttribute("href")).toBe("/events");
  expect(screen.getByRole("link", { name: "Servers" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Rules" })).toBeTruthy();
  expect(screen.getByRole("link", { name: "Activity" })).toBeTruthy();
  for (const label of ["Machines", "Providers", "Workload backups", "Machine snapshots", "Controller backups", "Credentials", "Assignments", "Receipts"]) {
    expect(screen.queryByRole("link", { name: label })).toBeNull();
  }
});

it.each<{ key: UIFeatureKey; labels: string[]; recovery?: string; automation?: string }>([
  { key: "machineProvisioning", labels: ["Machines", "Providers"] },
  { key: "machineSnapshots", labels: ["Machine snapshots"], recovery: "/recovery/machine-snapshots" },
  { key: "workloadBackups", labels: ["Workload backups"], recovery: "/recovery/workload-backups" },
  { key: "automationCredentials", labels: ["Credentials"], automation: "/automation/credentials" },
  { key: "infrastructureAssignments", labels: ["Assignments"], automation: "/automation/assignments" },
  { key: "mutationReceipts", labels: ["Receipts"], automation: "/automation/receipts" },
])("enables $key independently and uses an available default destination", ({ key, labels, recovery, automation }) => {
  render(navigation({ ...overview, controllerSettings: { operationsEnabled: false, uiFeatures: { [key]: true } } }));
  for (const label of ["Machines", "Providers", "Workload backups", "Machine snapshots", "Credentials", "Assignments", "Receipts"]) {
    const link = screen.queryByRole("link", { name: label });
    if (labels.includes(label)) expect(link).toBeTruthy();
    else expect(link).toBeNull();
  }
  const primary = within(screen.getByRole("complementary", { name: "Primary navigation" }));
  if (recovery) expect(primary.getByRole("link", { name: "Recovery" }).getAttribute("href")).toBe(recovery);
  else expect(primary.queryByRole("link", { name: "Recovery" })).toBeNull();
  expect(primary.getByRole("link", { name: "Automation" }).getAttribute("href")).toBe(automation || "/events");
});

it("keeps controller backups on their existing Operations flag", () => {
  render(navigation({ ...overview, controllerSettings: { operationsEnabled: true } }));
  const primary = within(screen.getByRole("complementary", { name: "Primary navigation" }));
  expect(primary.getByRole("link", { name: "Recovery" }).getAttribute("href")).toBe("/recovery/controller-backups");
  expect(screen.getByRole("link", { name: "Controller backups" })).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Workload backups" })).toBeNull();
});

it("retains owner and project access checks when features are enabled", () => {
  const member = { ...overview, identity: { ...overview.identity!, systemRole: "member" as const }, controllerSettings: { operationsEnabled: true, uiFeatures: { machineProvisioning: true, automationCredentials: true, infrastructureAssignments: true, mutationReceipts: true } } };
  render(navigation(member));
  for (const label of ["Providers", "Credentials", "Assignments", "Controller backups"]) expect(screen.queryByRole("link", { name: label })).toBeNull();
  const primary = within(screen.getByRole("complementary", { name: "Primary navigation" }));
  expect(primary.getByRole("link", { name: "Automation" }).getAttribute("href")).toBe("/automation/receipts");
  expect(primary.queryByRole("link", { name: "Recovery" })).toBeNull();
});
