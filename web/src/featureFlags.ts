import type { Overview, UIFeatureKey } from "./api/overview";
import type { ResourceSection } from "./routes";

export type { UIFeatureKey } from "./api/overview";

export const uiFeatures: ReadonlyArray<{ key: UIFeatureKey; label: string; description: string; validation: string }> = [
  {
    key: "machineProvisioning",
    label: "Machine provisioning",
    description: "Show machines and providers, including machine creation, deletion, and provider registration. Real provider allocation still needs validation.",
    validation: "Needs provider validation",
  },
  {
    key: "machineSnapshots",
    label: "Machine snapshots",
    description: "Show snapshot capture, deletion, and restore into an isolated clone. Capture and restore still need validation with a real provider.",
    validation: "Needs provider validation",
  },
  {
    key: "workloadBackups",
    label: "Workload backups",
    description: "Show database backup, restore, verification, and archive deletion. These actions still need a full browser check against a running database.",
    validation: "Needs browser validation",
  },
  {
    key: "automationCredentials",
    label: "Automation credentials",
    description: "Show automation accounts, credential issue, rotation, and revocation, and project permissions. The full credential lifecycle still needs a browser check.",
    validation: "Needs browser validation",
  },
  {
    key: "infrastructureAssignments",
    label: "Project assignments",
    description: "Show project resource assignments, including adding and removing access to targets, providers, SSH keys, and service templates. These changes still need a browser check.",
    validation: "Needs browser validation",
  },
  {
    key: "mutationReceipts",
    label: "Request receipts",
    description: "Show receipt lookup and linked operation details. Following a request through completion still needs a browser check.",
    validation: "Needs browser validation",
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
