import type { Overview, UIFeatureKey } from "./api/overview";
import type { ResourceSection } from "./routes";

export type { UIFeatureKey } from "./api/overview";

export const uiFeatures: ReadonlyArray<{ key: UIFeatureKey; label: string; description: string }> = [
  {
    key: "machineProvisioning",
    label: "Machine provisioning",
    description: "Create and delete machines, and manage infrastructure providers.",
  },
  {
    key: "machineSnapshots",
    label: "Machine snapshots",
    description: "Create snapshots and restore them as isolated machines.",
  },
  {
    key: "workloadBackups",
    label: "Workload backups",
    description: "Back up PostgreSQL databases, restore data, and manage backup archives.",
  },
  {
    key: "automationCredentials",
    label: "Automation credentials",
    description: "Manage automation accounts, API credentials, and their project permissions.",
  },
  {
    key: "infrastructureAssignments",
    label: "Project assignments",
    description: "Choose which targets, providers, SSH keys, and service templates each project can use.",
  },
  {
    key: "mutationReceipts",
    label: "Request receipts",
    description: "Look up a request by receipt ID and check its operation status.",
  },
];

export function isUIFeatureEnabled(overview: Overview, key: UIFeatureKey): boolean {
  return overview.controllerSettings?.uiFeatures?.[key] === true;
}

export const resourceUIFeature: Partial<Record<ResourceSection, UIFeatureKey>> = {
  machines: "machineProvisioning",
  providers: "machineProvisioning",
  "machine-snapshots": "machineSnapshots",
  "workload-backups": "workloadBackups",
  credentials: "automationCredentials",
  assignments: "infrastructureAssignments",
  receipts: "mutationReceipts",
};
