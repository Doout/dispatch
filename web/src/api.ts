import { accessApi } from "./api/access";
import { analyticsApi } from "./api/analytics";
import { applicationsApi } from "./api/applications";
import { backupsApi } from "./api/backups";
import { connectionsApi } from "./api/connections";
import { deploymentsApi } from "./api/deployments";
import { eventsApi } from "./api/events";
import { networksApi } from "./api/networks";
import { overviewApi } from "./api/overview";
import { previewsApi } from "./api/previews";
import { projectsApi } from "./api/projects";
import { secretsApi } from "./api/secrets";
import { serversApi } from "./api/servers";
import { servicesApi } from "./api/services";
import { workflowsApi } from "./api/workflows";

export {
  getToken,
  getImpersonatedUserID,
  setImpersonatedUserID,
  setToken,
  request,
  destructiveRequest,
} from "./api/transport";
export type * from "./api/access";
export type * from "./api/analytics";
export type * from "./api/applications";
export type * from "./api/backups";
export type * from "./api/connections";
export type * from "./api/deployments";
export type * from "./api/events";
export type * from "./api/networks";
export type * from "./api/overview";
export type * from "./api/previews";
export type * from "./api/projects";
export type * from "./api/secrets";
export type * from "./api/servers";
export type * from "./api/services";
export type * from "./api/workflows";

// The facade remains the single public client object used by components and tests.
export const api = {
  ...accessApi,
  ...analyticsApi,
  ...applicationsApi,
  ...backupsApi,
  ...connectionsApi,
  ...deploymentsApi,
  ...eventsApi,
  ...networksApi,
  ...overviewApi,
  ...previewsApi,
  ...projectsApi,
  ...secretsApi,
  ...serversApi,
  ...servicesApi,
  ...workflowsApi,
};
