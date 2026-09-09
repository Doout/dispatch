import { ConfigSourceError } from "./ConfigSourceError";
import { ArrowLeft } from "@phosphor-icons/react";
import {
  ConfigSource,
  Overview,
  WorkflowResource,
  WorkflowStageRun,
  WorkflowTopology,
} from "../api";
import { PageHeader } from "../PageHeader";
import { TopologyCanvas } from "./TopologyCanvas";
import { workflowResourceStatus, workflowResourceStatusLabel } from "./status";

type StageSelection = { resourceID: string; stageName: string };

export function ConfigurationTopologyPage({
  source,
  overview,
  onBack,
  onOpenResource,
  onOpenStage,
}: {
  source: ConfigSource;
  overview: Overview;
  onBack: () => void;
  onOpenResource: (resource: WorkflowResource) => void;
  onOpenStage: (resourceID: string, stageName: string) => void;
}) {
  const resources = (overview.workflowResources ?? []).filter((resource) => resource.configSourceId === source.id);
  const { topology, resourcesByNodeID, stagesByNodeID } = configurationTopology(source, resources, overview);

  return <div className="page-layout topology-page configuration-topology-page">
    <PageHeader view="applications" title={source.name} action={{ label: "Back", onClick: onBack, icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <div className="topology-context configuration-topology-context">
      <div>
        <span className={`status-label ${source.state}`}><i />{source.state === "invalid" ? "Sync blocked" : source.state === "degraded" ? "Sync warning" : workflowResourceStatusLabel(source.state)}</span>
        <span>{repositoryLabel(source.repository)} · {source.branch}</span>
        <code>{source.path || "Repository root"}</code>
      </div>
      <small>Select an application or stage to drill down.</small>
    </div>
    <ConfigSourceError source={source} />
    <TopologyCanvas
      topology={topology}
      label={`${source.name} configuration topology`}
      isNodeSelectable={(node) => resourcesByNodeID.has(node.id) || stagesByNodeID.has(node.id)}
      onNodeSelect={(node) => {
        const resource = resourcesByNodeID.get(node.id);
        if (resource) {
          onOpenResource(resource);
          return;
        }
        const stage = stagesByNodeID.get(node.id);
        if (stage) onOpenStage(stage.resourceID, stage.stageName);
      }}
    />
  </div>;
}

export function configurationTopology(source: ConfigSource, resources: WorkflowResource[], overview: Overview): {
  topology: WorkflowTopology;
  resourcesByNodeID: Map<string, WorkflowResource>;
  stagesByNodeID: Map<string, StageSelection>;
} {
  const revisions = overview.workflowRevisions ?? [];
  const stageRuns = overview.workflowStageRuns ?? [];
  const sourceNodeID = `configuration:${source.id}`;
  const resourcesByNodeID = new Map<string, WorkflowResource>();
  const stagesByNodeID = new Map<string, StageSelection>();
  const nodes: WorkflowTopology["nodes"] = [{
    id: sourceNodeID,
    column: "configuration",
    kind: "source",
    label: source.name,
    detail: repositoryLabel(source.repository),
    state: source.state,
    metadata: {
      Branch: source.branch,
      Path: source.path || "Repository root",
    },
  }];
  const edges: WorkflowTopology["edges"] = [];

  resources.forEach((resource) => {
    const resourceNodeID = `application:${resource.id}`;
    const resourceRevisions = revisions.filter((revision) => revision.resourceId === resource.id).sort(newestFirst);
    const latestRevision = resourceRevisions[0];
    const resourceStatus = workflowResourceStatus(resource, latestRevision?.state);
    resourcesByNodeID.set(resourceNodeID, resource);
    nodes.push({
      id: resourceNodeID,
      column: "applications",
      kind: resource.kind.toLowerCase(),
      label: resource.name,
      detail: `${resource.sourceCount} source${resource.sourceCount === 1 ? "" : "s"}, ${resource.jobCount} job${resource.jobCount === 1 ? "" : "s"}`,
      state: workflowResourceStatusLabel(resourceStatus),
      metadata: {
        File: resource.path,
        Revision: workflowRevisionLabel(latestRevision?.configSha ?? resource.configSha),
      },
    });
    edges.push({ from: sourceNodeID, to: resourceNodeID, kind: "owns" });

    workflowStages(resource, overview).forEach((stage) => {
      const stageNodeID = `stage:${resource.id}:${stage.name}`;
      stagesByNodeID.set(stageNodeID, { resourceID: resource.id, stageName: stage.name });
      nodes.push({
        id: stageNodeID,
        column: "stages",
        kind: "stage",
        label: stage.label,
        detail: stage.target,
        state: workflowStageStatusLabel(stage.state),
        metadata: { Revision: stage.revision },
      });
      edges.push({ from: resourceNodeID, to: stageNodeID, kind: "runs" });
    });
  });

  return {
    topology: {
      columns: [
        { id: "configuration", label: "Configuration" },
        { id: "applications", label: "Applications" },
        { id: "stages", label: "Stages" },
      ],
      nodes,
      edges,
    },
    resourcesByNodeID,
    stagesByNodeID,
  };
}

export type WorkflowStageInventory = {
  name: string;
  label: string;
  target: string;
  state: string;
  revision: string;
  run?: WorkflowStageRun;
  deploymentID?: string;
};

export function workflowStages(resource: WorkflowResource, overview: Overview): WorkflowStageInventory[] {
  const revisions = (overview.workflowRevisions ?? []).filter((revision) => revision.resourceId === resource.id).sort(newestFirst);
  const revisionIDs = new Set(revisions.map((revision) => revision.id));
  const revisionByID = new Map(revisions.map((revision) => [revision.id, revision]));
  const runs = (overview.workflowStageRuns ?? []).filter((run) => revisionIDs.has(run.revisionId)).sort(newestFirst);
  const names = [...new Set([...(resource.stageNames ?? []), ...runs.map((run) => run.stageName)])];

  return names.map((name, index) => {
    const run = runs.find((item) => item.stageName === name);
    const revision = run ? revisionByID.get(run.revisionId) : revisions[0];
    const deploymentID = run?.deploymentIds
      ?.flatMap((id) => {
        const deployment = overview.deployments.find((item) => item.id === id);
        return deployment ? [deployment] : [];
      })
      .sort(newestFirst)[0]?.id;
    return {
      name,
      label: humanize(name),
      target: run?.targetRef || resource.targetRefs?.[index] || "Not set",
      state: run?.state ?? (resource.active ? "not_deployed" : "pending_activation"),
      revision: workflowRevisionLabel(revision?.configSha ?? resource.configSha),
      run,
      deploymentID,
    };
  });
}

export function workflowStageStatusLabel(state: string) {
  if (state === "succeeded") return "Ready";
  if (state === "not_deployed") return "Not deployed";
  if (state === "pending_activation") return "Pending activation";
  if (state === "awaiting_approval") return "Awaiting approval";
  return humanize(state);
}

function workflowRevisionLabel(value: string) {
  return value ? value.slice(0, 8) : "No revision";
}

function humanize(value: string) {
  const text = value.replaceAll("_", " ").replaceAll("-", " ");
  return text ? `${text.charAt(0).toUpperCase()}${text.slice(1)}` : value;
}

function repositoryLabel(repository: string) {
  return repository.replace(/^[^:]+@[^:]+:/, "").replace(/^https?:\/\/[^/]+\//, "").replace(/\.git$/, "").split("/").filter(Boolean).slice(-2).join("/");
}

function newestFirst(left: { createdAt: string }, right: { createdAt: string }) {
  return right.createdAt.localeCompare(left.createdAt);
}
