import { PencilSimple, Trash } from "@phosphor-icons/react";
import { Overview, Project } from "../api";
import { PageHeader } from "../PageHeader";
import { TableIconAction } from "../ResourceTable";
import { canManageProject } from "../permissions";
import { ResourceSummary, EmptyState } from "../components/PageStates";

export function ProjectsPage({
  overview,
  canCreate = true,
  onAdd,
  onEdit,
  onDelete,
}: {
  overview: Overview;
  canCreate?: boolean;
  onAdd: () => void;
  onEdit: (project: Project) => void;
  onDelete: (project: Project) => void;
}) {
  return (
    <div className="page-layout">
      <PageHeader
        view="projects"
        action={canCreate ? { label: "Add project", onClick: onAdd } : undefined}
      />
      <ResourceSummary
        items={[
          { label: "Projects", value: overview.projects.length },
          { label: "Applications", value: overview.apps.length },
        ]}
      />
      {overview.projects.length ? (
        <div className="resource-table-wrap">
          <table className="resource-table">
            <thead>
              <tr>
                <th>Project</th>
                <th>Description</th>
                <th>Applications</th>
                <th>Created</th>
                <th className="actions-head">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {overview.projects.map((project) => (
                <tr key={project.id}>
                  <td data-label="Project">
                    <strong>{project.name}</strong>
                  </td>
                  <td data-label="Description">
                    {project.description || (
                      <span className="muted-value">None</span>
                    )}
                  </td>
                  <td data-label="Applications">
                    {
                      overview.apps.filter(
                        (app) => app.projectId === project.id,
                      ).length
                    }
                  </td>
                  <td data-label="Created">
                    {new Date(project.createdAt).toLocaleDateString()}
                  </td>
                  <td className="row-actions">
                    {canManageProject(overview, project.id, "project.manage") && <div className="table-icon-actions">
                      <TableIconAction
                        label={`Edit ${project.name}`}
                        tooltip="Edit"
                        onClick={() => onEdit(project)}
                      >
                        <PencilSimple size={16} />
                      </TableIconAction>
                      <TableIconAction
                        label={`Delete ${project.name}`}
                        tooltip="Delete"
                        danger
                        onClick={() => onDelete(project)}
                      >
                        <Trash size={16} />
                      </TableIconAction>
                    </div>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState
          title="No projects"
          action={canCreate ? { label: "Add project", onClick: onAdd } : undefined}
        />
      )}
    </div>
  );
}
