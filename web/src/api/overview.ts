import { request } from "./transport";
import type { Project } from "./projects";
import type { Permission, Identity } from "./access";
import type { Server, RelayWebhook } from "./servers";
import type { App } from "./applications";
import type { Deployment } from "./deployments";
import type { PreviewGroup, PreviewGroupRun, PreviewEnvironment } from "./previews";
import type { ConfigSource, GitHubAppConnection } from "./connections";
import type {
  WorkflowResource,
  WorkflowPreviewTemplate,
  WorkflowRevision,
  WorkflowStageRun,
} from "./workflows";
import type { Secret, SecretStore } from "./secrets";
import type { PrivateNetwork } from "./networks";
import type { EventTrigger } from "./events";
import type { ServiceConnection } from "./services";

export type UIFeatureKey = "machineProvisioning" | "machineSnapshots" | "workloadBackups" | "automationCredentials" | "infrastructureAssignments" | "mutationReceipts";

export type ControllerSettings = {
  operationsEnabled: boolean;
  uiFeatures?: Partial<Record<UIFeatureKey, boolean>>;
};

export type Overview = {
  controllerSettings?: ControllerSettings;
  services?: ServiceConnection[];
  demo: boolean;
  secretStorageConfigured: boolean;
  identity?: Identity;
  impersonator?: Identity;
  projectPermissions?: Record<string, Permission[]>;
  projects: Project[];
  servers: Server[];
  apps: App[];
  deployments: Deployment[];
  eventTriggers: EventTrigger[];
  previews: PreviewEnvironment[];
  previewGroups: PreviewGroup[];
  previewGroupRuns: PreviewGroupRun[];
  secrets: Secret[];
  secretStores?: SecretStore[];
  privateNetworks?: PrivateNetwork[];
  githubApps: GitHubAppConnection[];
  relayWebhooks: RelayWebhook[];
  configSources?: ConfigSource[];
  workflowPreviewTemplates?: WorkflowPreviewTemplate[];
  workflowResources?: WorkflowResource[];
  workflowRevisions?: WorkflowRevision[];
  workflowStageRuns?: WorkflowStageRun[];
};

export const overviewApi = {
  overview: () => request<Overview>("/api/v1/overview"),
};
