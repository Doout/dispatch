import type { Overview, Permission } from "./api";

export function canManageController(
  overview: Overview,
  permission: Permission,
) {
  return (
    overview.identity?.systemRole === "owner" ||
    overview.identity?.permissions?.includes(permission) === true
  );
}

export function canManageProject(
  overview: Overview,
  projectID: string,
  permission: Permission,
) {
  return (
    overview.identity?.systemRole === "owner" ||
    overview.projectPermissions?.[projectID]?.includes(permission) === true
  );
}

export function canManageAnyProject(
  overview: Overview,
  permission: Permission,
) {
  return overview.projects.some((project) =>
    canManageProject(overview, project.id, permission),
  );
}
