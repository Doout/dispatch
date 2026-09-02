import { useEffect, useMemo, useState } from "react";
import { ArrowSquareOut, CaretDown, CaretRight, CaretUp, CheckCircle, Circle, CircleNotch, GitBranch, WarningCircle } from "@phosphor-icons/react";
import type { App, Deployment, Overview, WorkflowResource, WorkflowRevision, WorkflowStageRun } from "../api";
import { relative, short } from "../presentation";
import { routePath, shouldHandleNavigation } from "../routes";

type RailTone = "success" | "danger" | "active" | "waiting";

type RailSource = {
  detail: string;
  revision: string;
  title: string;
};

type RailStage = {
  name: string;
  label: string;
  target: string;
  state: string;
  tone: RailTone;
  revision: string;
  revisionTitle: string;
  candidateRevision?: string;
  candidateRevisionTitle?: string;
  trigger: string;
  currentRevision: boolean;
  run?: WorkflowStageRun;
  deployment?: Deployment;
  deployments: Deployment[];
  updatedAt?: string;
  error?: string;
};

export type DeploymentRail = {
  id: string;
  name: string;
  detail: string;
  source: RailSource;
  stages: RailStage[];
};

export function buildDeploymentRails(overview: Overview): DeploymentRail[] {
  const resources = (overview.workflowResources ?? []).filter((resource) => resource.kind === "Application");
  const revisions = overview.workflowRevisions ?? [];
  const stageRuns = overview.workflowStageRuns ?? [];
  const deploymentByID = new Map(overview.deployments.map((deployment) => [deployment.id, deployment]));
  const managedAppIDs = new Set<string>();

  for (const stage of stageRuns) {
    for (const deploymentID of stage.deploymentIds ?? []) {
      const appID = deploymentByID.get(deploymentID)?.appId;
      if (appID) managedAppIDs.add(appID);
    }
  }

  const managedRails = resources.map((resource) => buildManagedRail(resource, revisions, stageRuns, deploymentByID));
  const directRails = overview.apps
    .filter((application) => !application.template && !application.generated && !managedAppIDs.has(application.id))
    .map((application) => buildDirectRail(application, overview.deployments, overview));

  return [...managedRails, ...directRails].sort((left, right) => railPriority(left) - railPriority(right) || left.name.localeCompare(right.name));
}

function buildManagedRail(resource: WorkflowResource, allRevisions: WorkflowRevision[], allStageRuns: WorkflowStageRun[], deploymentByID: Map<string, Deployment>): DeploymentRail {
  const revisions = allRevisions.filter((revision) => revision.resourceId === resource.id).sort(sortNewest);
  const revisionByID = new Map(revisions.map((revision) => [revision.id, revision]));
  const runs = allStageRuns.filter((stage) => revisionByID.has(stage.revisionId)).sort(sortNewest);
  const latestRevision = revisions[0];
  const stageNames = unique([...(resource.stageNames ?? []), ...runs.map((stage) => stage.stageName)]);
  if (stageNames.length === 0) stageNames.push("deployment");

  const stages = stageNames.map((name, index) => {
    const stageRuns = runs.filter((stage) => stage.stageName === name);
    const run = stageRuns[0];
    const candidateRevision = run ? revisionByID.get(run.revisionId) : undefined;
    const deployedRun = stageRuns.find((stage) => (stage.deploymentIds?.length ?? 0) > 0);
    const deployedRevision = deployedRun ? revisionByID.get(deployedRun.revisionId) : undefined;
    const deployments = unique(stageRuns.flatMap((stage) => stage.deploymentIds ?? []))
      .map((id) => deploymentByID.get(id))
      .filter((deployment): deployment is Deployment => Boolean(deployment))
      .sort(sortNewest);
    const deployment = deployedRun?.deploymentIds?.map((id) => deploymentByID.get(id)).filter((item): item is Deployment => Boolean(item)).sort(sortNewest)[0] ?? deployments[0];
    const state = run?.state ?? deployment?.state ?? "not_deployed";
    return {
      name,
      label: humanize(name),
      target: run?.targetRef || resource.targetRefs?.[index] || humanize(name),
      state,
      tone: stateTone(state),
      revision: revisionSummary(deployedRevision),
      revisionTitle: revisionTitle(deployedRevision),
      candidateRevision: candidateRevision && candidateRevision.id !== deployedRevision?.id ? revisionSummary(candidateRevision) : undefined,
      candidateRevisionTitle: candidateRevision && candidateRevision.id !== deployedRevision?.id ? revisionTitle(candidateRevision) : undefined,
      trigger: candidateRevision?.trigger || deployedRevision?.trigger || "workflow",
      currentRevision: Boolean(deployedRevision && latestRevision && deployedRevision.id === latestRevision.id),
      run,
      deployment,
      deployments,
      updatedAt: run?.finishedAt ?? run?.startedAt ?? run?.createdAt ?? deployment?.finishedAt ?? deployment?.createdAt,
      error: run?.error || (deployment?.state === "failed" ? deployment.message : undefined),
    } satisfies RailStage;
  });

  const sourceCount = latestRevision ? Object.keys(latestRevision.sources).length : resource.sourceCount;
  return {
    id: resource.id,
    name: resource.name,
    detail: `${sourceCount} source${sourceCount === 1 ? "" : "s"} · ${resource.jobCount} job${resource.jobCount === 1 ? "" : "s"}`,
    source: {
      detail: sourceCount === 1 ? sourceBranch(latestRevision) : `${sourceCount} repositories`,
      revision: revisionSummary(latestRevision),
      title: revisionTitle(latestRevision),
    },
    stages,
  };
}

function buildDirectRail(application: App, allDeployments: Deployment[], overview: Overview): DeploymentRail {
  const deployments = allDeployments.filter((deployment) => deployment.appId === application.id).sort(sortNewest);
  const deployment = deployments[0];
  const target = overview.servers.find((server) => server.id === application.serverId)?.name ?? deployment?.server?.name ?? "Target";
  const state = deployment?.state ?? "not_deployed";
  const revision = deployment ? short(deployment.commitSha) : "No revision";
  return {
    id: application.id,
    name: application.name,
    detail: application.buildType === "helm" ? "Helm" : application.buildType === "compose" ? "Compose" : "Dockerfile",
    source: { detail: application.branch || "Default branch", revision, title: deployment?.commitSha ?? revision },
    stages: [{
      name: target,
      label: humanize(target),
      target,
      state,
      tone: stateTone(state),
      revision,
      revisionTitle: deployment?.commitSha ?? revision,
      trigger: "manual",
      currentRevision: true,
      deployment,
      deployments,
      updatedAt: deployment?.finishedAt ?? deployment?.createdAt,
      error: deployment?.state === "failed" ? deployment.message : undefined,
    }],
  };
}

export function DeploymentFocusRails({ overview, selectedApplicationID, selectedStageName, onSelectStage, onSelectDeployment }: {
  overview: Overview;
  selectedApplicationID?: string;
  selectedStageName?: string;
  onSelectStage?: (applicationID?: string, stageName?: string) => void;
  onSelectDeployment: (id: string) => void;
}) {
  const rails = useMemo(() => buildDeploymentRails(overview), [overview]);
  const [localSelection, setLocalSelection] = useState<{ applicationID: string; stageName: string } | null>(null);

  useEffect(() => {
    setLocalSelection(selectedApplicationID ? { applicationID: selectedApplicationID, stageName: selectedStageName ?? "" } : null);
  }, [selectedApplicationID, selectedStageName]);

  if (rails.length === 0) return null;

  return <div className="deployment-focus-rails">
    {rails.map((rail) => {
      const expanded = localSelection?.applicationID === rail.id;
      const requestedStage = expanded ? localSelection.stageName : undefined;
      const selectedStage = rail.stages.find((stage) => stage.name === requestedStage) ?? preferredStage(rail.stages);
      return <DeploymentFocusRail
        key={rail.id}
        rail={rail}
        expanded={expanded}
        selectedStage={selectedStage}
        onToggle={() => {
          if (expanded) {
            setLocalSelection(null);
            onSelectStage?.();
            return;
          }
          setLocalSelection({ applicationID: rail.id, stageName: selectedStage.name });
          onSelectStage?.(rail.id, selectedStage.name);
        }}
        onSelectStage={(stageName) => {
          setLocalSelection({ applicationID: rail.id, stageName });
          onSelectStage?.(rail.id, stageName);
        }}
        onSelectDeployment={onSelectDeployment}
      />;
    })}
  </div>;
}

function DeploymentFocusRail({ rail, expanded, selectedStage, onToggle, onSelectStage, onSelectDeployment }: {
  rail: DeploymentRail;
  expanded: boolean;
  selectedStage: RailStage;
  onToggle: () => void;
  onSelectStage: (stageName: string) => void;
  onSelectDeployment: (id: string) => void;
}) {
  const readyStages = rail.stages.filter((stage) => stage.state === "succeeded").length;
  const contentID = `deployment-application-${safeID(rail.id)}-details`;
  const headingID = `deployment-application-${safeID(rail.id)}-heading`;
  const summaryState = railSummaryState(rail.stages);
  return <article className={`deployment-focus-card ${expanded ? "expanded" : "compact"}`} id={`deployment-application-${safeID(rail.id)}`} aria-labelledby={headingID}>
    <header className="deployment-focus-heading">
      <div className="deployment-focus-identity"><h2 id={headingID}>{rail.name}</h2><span>{rail.detail}</span></div>
      <CompactPromotionSummary rail={rail} />
      <div className="deployment-focus-actions">
        <RailStatus state={summaryState} tone={stateTone(summaryState)} />
        <span className="deployment-stage-count">{readyStages}/{rail.stages.length} ready</span>
        <span className="deployment-disclosure-icon" aria-hidden="true">
          {expanded ? <CaretUp size={15} /> : <CaretDown size={15} />}
        </span>
      </div>
      <button
        type="button"
        className="deployment-focus-toggle"
        aria-label={`${expanded ? "Collapse" : "Expand"} ${rail.name} deployment`}
        aria-expanded={expanded}
        aria-controls={contentID}
        onClick={onToggle}
      />
    </header>

    {expanded && <div id={contentID}>
      <div className="deployment-promotion-rail" aria-label={`${rail.name} promotion stages`}>
        <div className="deployment-source-node">
          <span className="deployment-node-icon"><GitBranch size={17} /></span>
          <span><strong>Source</strong><small title={rail.source.title}>{rail.source.detail}</small></span>
          <code title={rail.source.title}>{rail.source.revision}</code>
        </div>
        {rail.stages.map((stage) => <button
          key={stage.name}
          type="button"
          className={`deployment-stage-node ${stage.tone} ${stage.name === selectedStage.name ? "selected" : ""}`}
          aria-pressed={stage.name === selectedStage.name}
          aria-label={`${stage.label}, ${stateLabel(stage.state)}, target ${stage.target}, revision ${stage.revision}`}
          onClick={() => onSelectStage(stage.name)}
        >
          <span className="deployment-stage-node-main"><strong>{stage.label}</strong><RailStatus state={stage.state} tone={stage.tone} /></span>
          <span className="deployment-node-meta"><span>{stage.target}</span><code title={stage.revisionTitle}>{stage.revision}</code></span>
          {!stage.currentRevision && stage.run && <small className="deployment-revision-drift" title={stage.candidateRevisionTitle ? `Next revision: ${stage.candidateRevisionTitle}` : undefined}>{stage.deployment ? "Older revision" : "Not deployed"}</small>}
        </button>)}
      </div>
      <StageInspector rail={rail} stage={selectedStage} onSelectDeployment={onSelectDeployment} />
    </div>}
  </article>;
}

function CompactPromotionSummary({ rail }: { rail: DeploymentRail }) {
  return <div className="deployment-compact-route" aria-label={`${rail.name} deployment summary`}>
    <div className="deployment-compact-source">
      <GitBranch size={15} aria-hidden="true" />
      <span><strong>Source</strong><code title={rail.source.title}>{rail.source.revision}</code></span>
    </div>
    {rail.stages.map((stage) => <div className={`deployment-compact-stage ${stage.tone}`} key={stage.name}>
      <CaretRight size={13} aria-hidden="true" />
      <RailStatusIcon tone={stage.tone} />
      <span><strong>{stage.label}</strong><small>{stage.target}</small></span>
      <code title={stage.revisionTitle}>{stage.revision}</code>
    </div>)}
  </div>;
}

function StageInspector({ rail, stage, onSelectDeployment }: { rail: DeploymentRail; stage: RailStage; onSelectDeployment: (id: string) => void }) {
  const [showAllRuns, setShowAllRuns] = useState(false);
  const deployState = stage.run && stage.run.state !== "succeeded" ? stage.run.state : stage.deployment?.state ?? stage.run?.state ?? "not_deployed";
  const readyState = stage.state === "succeeded" ? "Ready" : stage.state === "failed" ? "Failed" : stage.state === "awaiting_approval" ? "Waiting" : "Pending";

  useEffect(() => setShowAllRuns(false), [rail.id, stage.name]);

  return <section className="deployment-stage-inspector" aria-label={`${rail.name} ${stage.label} deployment details`}>
    <div className="deployment-stage-summary">
      <header><div><h3>{stage.label}</h3><RailStatus state={stage.state} tone={stage.tone} /></div>{stage.deployment && <DeploymentLink deployment={stage.deployment} onSelect={onSelectDeployment} label={`Open ${rail.name} ${stage.label} deployment`} />}</header>
      <dl>
        <div><dt>Target</dt><dd>{stage.target}</dd></div>
        <div><dt>Revision</dt><dd><code title={stage.revisionTitle}>{stage.revision}</code>{!stage.currentRevision && stage.run && <span title={stage.candidateRevisionTitle}>{stage.candidateRevision ? `Next ${stage.candidateRevision}` : "Behind source"}</span>}</dd></div>
        <div><dt>Updated</dt><dd>{stage.updatedAt ? relative(stage.updatedAt) : "Not deployed"}</dd></div>
        <div><dt>Trigger</dt><dd>{humanize(stage.trigger)}</dd></div>
      </dl>
      {stage.error && <p className="deployment-stage-error" title={stage.error}>{stage.error}</p>}
      <ol className="deployment-stage-progress" aria-label={`${stage.label} progress`}>
        <ProgressStep
          label="Source"
          value={rail.source.revision}
          tone={rail.source.revision === "No revision" ? "waiting" : "success"}
        />
        <ProgressStep label="Deploy" value={stateLabel(deployState)} tone={stateTone(deployState)} />
        <ProgressStep label="Ready" value={readyState} tone={stage.state === "succeeded" ? "success" : stage.state === "failed" ? "danger" : "waiting"} />
      </ol>
    </div>

    <div className="deployment-recent-runs">
      <header><h3>Recent runs</h3><div><span>{stage.deployments.length}</span>{stage.deployments.length > 4 && <button type="button" aria-expanded={showAllRuns} aria-controls={`deployment-${safeID(rail.id)}-${safeID(stage.name)}-runs`} onClick={() => setShowAllRuns((current) => !current)}>{showAllRuns ? "Show less" : "View all"}</button>}</div></header>
      {stage.deployments.length === 0 ? <p>No deployments yet.</p> : <ul id={`deployment-${safeID(rail.id)}-${safeID(stage.name)}-runs`} className={showAllRuns ? "all" : undefined} aria-label={`${rail.name} ${stage.label} recent runs`}>{stage.deployments.map((deployment) => <li key={deployment.id}>
        <DeploymentLink deployment={deployment} onSelect={onSelectDeployment} label={`${rail.name} ${stage.label} ${short(deployment.commitSha)} deployment`} compact />
      </li>)}</ul>}
    </div>
  </section>;
}

function DeploymentLink({ deployment, onSelect, label, compact = false }: { deployment: Deployment; onSelect: (id: string) => void; label: string; compact?: boolean }) {
  const tone = stateTone(deployment.state);
  return <a
    className={compact ? `deployment-run-link ${tone}` : "deployment-open-link"}
    href={routePath({ view: "deployments", deploymentID: deployment.id })}
    aria-label={label}
    onClick={(event) => {
      if (!shouldHandleNavigation(event)) return;
      event.preventDefault();
      onSelect(deployment.id);
    }}
  >
    {compact && <RailStatusIcon tone={tone} />}
    {compact && <code>{short(deployment.commitSha)}</code>}
    {compact && <span>{stateLabel(deployment.state)}</span>}
    {compact && <time dateTime={deployment.finishedAt ?? deployment.createdAt}>{relative(deployment.finishedAt ?? deployment.createdAt)}</time>}
    {!compact && <><span>Open deployment</span><ArrowSquareOut size={14} /></>}
  </a>;
}

function ProgressStep({ label, value, tone }: { label: string; value: string; tone: RailTone }) {
  return <li className={tone}><RailStatusIcon tone={tone} /><span><strong>{label}</strong><small>{value}</small></span></li>;
}

function RailStatus({ state, tone }: { state: string; tone: RailTone }) {
  return <span className={`deployment-rail-status ${tone}`}><RailStatusIcon tone={tone} />{stateLabel(state)}</span>;
}

function RailStatusIcon({ tone }: { tone: RailTone }) {
  if (tone === "success") return <CheckCircle size={15} weight="fill" aria-hidden="true" />;
  if (tone === "danger") return <WarningCircle size={15} weight="fill" aria-hidden="true" />;
  if (tone === "active") return <CircleNotch size={15} weight="bold" aria-hidden="true" />;
  return <Circle size={15} aria-hidden="true" />;
}

function railPriority(rail: DeploymentRail) {
  if (rail.stages.some((stage) => stage.tone === "danger")) return 0;
  if (rail.stages.some((stage) => stage.tone === "active" || stage.state === "awaiting_approval")) return 1;
  if (rail.stages.some((stage) => stage.state === "not_deployed")) return 2;
  return 3;
}

function railSummaryState(stages: RailStage[]) {
  if (stages.some((stage) => stage.tone === "danger")) return "failed";
  const active = stages.find((stage) => stage.tone === "active");
  if (active) return active.state;
  if (stages.some((stage) => stage.state === "awaiting_approval")) return "awaiting_approval";
  if (stages.every((stage) => stage.state === "succeeded")) return "succeeded";
  return "not_deployed";
}

function preferredStage(stages: RailStage[]) {
  return stages.find((stage) => ["failed", "awaiting_approval", "queued", "fetching", "building", "starting", "checking", "routing", "running", "in_progress"].includes(stage.state))
    ?? [...stages].reverse().find((stage) => stage.run || stage.deployment)
    ?? stages[0];
}

function stateTone(state: string): RailTone {
  if (["succeeded", "ready", "live", "complete", "completed"].includes(state)) return "success";
  if (["failed", "cancelled", "error"].includes(state)) return "danger";
  if (["queued", "fetching", "building", "starting", "checking", "routing", "running", "in_progress"].includes(state)) return "active";
  return "waiting";
}

function stateLabel(state: string) {
  if (state === "succeeded") return "Ready";
  if (state === "awaiting_approval") return "Awaiting approval";
  if (state === "not_deployed") return "Not deployed";
  if (state === "in_progress") return "In progress";
  return humanize(state);
}

function revisionSummary(revision?: WorkflowRevision) {
  if (!revision) return "No revision";
  const sources = Object.values(revision.sources);
  if (sources.length === 1) return short(sources[0].commitSha);
  return short(revision.configSha);
}

function revisionTitle(revision?: WorkflowRevision) {
  if (!revision) return "No revision";
  const sources = Object.values(revision.sources);
  if (sources.length === 0) return revision.configSha;
  return sources.map((source) => `${source.alias}: ${source.commitSha}`).join("\n");
}

function sourceBranch(revision?: WorkflowRevision) {
  const source = revision ? Object.values(revision.sources)[0] : undefined;
  return source?.branch || "Repository";
}

function humanize(value: string) {
  const text = value.replaceAll("_", " ").replaceAll("-", " ").trim();
  return text ? text[0].toUpperCase() + text.slice(1) : value;
}

function safeID(value: string) {
  return value.replace(/[^a-zA-Z0-9_-]/g, "-");
}

function unique<T>(values: T[]) {
  return [...new Set(values)];
}

function sortNewest<T extends { createdAt: string }>(left: T, right: T) {
  return right.createdAt.localeCompare(left.createdAt);
}
