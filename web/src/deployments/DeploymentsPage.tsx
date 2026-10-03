import { PromotionWorkspace } from "./ReleaseTools";
import { catalogOverview, useDeploymentCatalog } from "./DeploymentCatalog";
import { DeploymentWorkspace } from "./DeploymentWorkspace";
import { useMemo, useState } from "react";
import { RocketLaunch } from "@phosphor-icons/react";
import { Deployment, Overview } from "../api";
import { DeploymentSection, DeploymentFilters } from "../routes";
import { PageHeader } from "../PageHeader";
import { buildDeploymentRails, DeploymentFocusRails } from "./DeploymentFocusRail";
import { canManageAnyProject, canManageProject } from "../permissions";
import type { Dialog } from "../app/dialogTypes";
import { EmptyState } from "../components/PageStates";

export function DeploymentsPage({
  overview: sourceOverview,
  filters: controlledFilters,
  onFilters,
  onChanged,
  selectedApplicationID,
  selectedStageName,
  onSelectStage,
  onSelect,
  onOpen,
  onCreateApplication,
}: {
  overview: Overview;
  filters?: DeploymentFilters;
  onFilters?: (filters: DeploymentFilters) => void;
  onChanged?: () => void | Promise<void>;
  selectedApplicationID?: string;
  selectedStageName?: string;
  onSelectStage?: (applicationID?: string, stageName?: string) => void;
  onSelect: (id: string, section?: DeploymentSection) => void;
  onOpen: (dialog: Dialog) => void;
  onCreateApplication: () => void;
}) {
  const catalog = useDeploymentCatalog();
  const overview = useMemo(() => catalogOverview(sourceOverview, catalog.items), [sourceOverview, catalog.items]);
  const [localFilters, setLocalFilters] = useState<DeploymentFilters>({});
  const filters = controlledFilters ?? localFilters;
  const changeFilters = onFilters ?? setLocalFilters;
  const rails = useMemo(() => buildDeploymentRails(overview), [overview]);
  const runnableApps = overview.apps.filter(
    (app) =>
      !app.template &&
      canManageProject(overview, app.projectId, "deployment.run"),
  );
  const ready = overview.servers.some(
    (server) => server.state === "ready" && server.runtime !== "relay" && server.runtime !== "builder",
  );
  const canAddApp =
    ready && canManageAnyProject(overview, "project.configure");
  const action =
    runnableApps.length > 0
      ? {
          label: "Deploy revision",
          onClick: () => onOpen("deploy"),
          icon: <RocketLaunch size={16} />,
        }
      : canAddApp
        ? { label: "Add application", onClick: onCreateApplication }
        : undefined;

  return (
    <div className="page-layout">
      <PageHeader view="deployments" action={action} />
      {rails.length > 0 && <section className="deployment-overview" aria-label="Deployment health">
        <div className="deployment-overview-title"><h2>Rollout status</h2><p>{rails.length} {rails.length === 1 ? "application" : "applications"}</p></div>
        <dl>
          <div><dd>{rails.flatMap((rail) => rail.stages).length}</dd><dt>Total stages</dt></div>
          <div className="success"><dd>{rails.flatMap((rail) => rail.stages).filter((stage) => stage.tone === "success").length}</dd><dt><i />Ready</dt></div>
          <div className="active"><dd>{rails.flatMap((rail) => rail.stages).filter((stage) => stage.tone === "active").length}</dd><dt><i />In progress</dt></div>
          <div className="danger"><dd>{rails.flatMap((rail) => rail.stages).filter((stage) => stage.tone === "danger").length}</dd><dt><i />Needs attention</dt></div>
        </dl>
      </section>}
      {overview.projects.map(project => {
        const sourceIDs = new Set((overview.configSources ?? []).filter(source => source.projectId === project.id).map(source => source.id));
        const resourceIDs = new Set((overview.workflowResources ?? []).filter(resource => sourceIDs.has(resource.configSourceId)).map(resource => resource.id));
        const revisions = (overview.workflowRevisions ?? []).filter(revision => resourceIDs.has(revision.resourceId));
        const revisionIDs = new Set(revisions.map(revision => revision.id));
        return <PromotionWorkspace key={project.id} stages={(overview.workflowStageRuns ?? []).filter(stage => revisionIDs.has(stage.revisionId))} revisions={revisions} canApprove={canManageProject(overview, project.id, "stage.approve")} onRefresh={onChanged} />;
      })}
      <DeploymentWorkspace overview={overview} filters={filters} onFilters={changeFilters} onOpen={onSelect}>{visible => <div className="deployment-workspace">
        <section className="deployment-board" aria-label="Deployment activity">
          {rails.length === 0 && (
            <EmptyState
              title="No deployment activity"
              body={
                runnableApps.length
                  ? "Deploy a revision to start."
                  : "Applications appear here after their first deployment."
              }
              action={action}
            />
          )}
          {rails.length > 0 && (
            <DeploymentFocusRails
              overview={overview}
              visibleItems={visible}
              selectedApplicationID={selectedApplicationID}
              selectedStageName={selectedStageName}
              onSelectStage={onSelectStage}
              onSelectDeployment={onSelect}
            />
          )}
        </section>
      </div>}</DeploymentWorkspace>
    </div>
  );
}
