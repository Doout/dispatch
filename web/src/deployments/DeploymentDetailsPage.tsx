import { ApplicationRoutePanel } from "../ApplicationRoute";
import { DeploymentHealth } from "../DeploymentHealth";
import { ReleaseTools } from "./ReleaseTools";
import { DeploymentIdentity } from "./DeploymentCatalog";
import { DeploymentHistory } from "./DeploymentHistory";
import { RuntimeSyncDisclosure } from "../ApplicationSync";
import { ReactNode, useState } from "react";
import { ArrowLeft, ArrowSquareOut, Check, Copy, X } from "@phosphor-icons/react";
import { Deployment, DeploymentLog, Overview, Server } from "../api";
import { relative, short, statusTone } from "../presentation";
import { DeploymentSection, routePath, shouldHandleNavigation } from "../routes";
import { PageHeader } from "../PageHeader";
import { useDialogFocus } from "../useDialogFocus";
import { DeploymentRuntime } from "./RuntimeTopology";
import { canManageProject } from "../permissions";
import { EmptyState } from "../components/PageStates";

export function DeploymentDetailsPage({
  overview,
  deployment,
  section = "summary",
  onSectionChange = () => undefined,
  onSelectDeployment,
  onOpenDeployment,
  logs,
  logsLoading,
  logsError,
  onBack,
  onCancel,
  canCancel = true,
}: {
  overview?: Overview;
  deployment: Deployment;
  section?: DeploymentSection;
  onSectionChange?: (section: DeploymentSection) => void;
  onSelectDeployment?: (id: string) => void | Promise<void>;
  onOpenDeployment?: (id: string) => void;
  logs: DeploymentLog[];
  logsLoading: boolean;
  logsError: string;
  onBack: () => void;
  onCancel: () => void;
  canCancel?: boolean;
}) {
  const titleID = `${deploymentEvidenceID(deployment.id)}-title`;
  return (
    <div className="page-layout deployment-details-page">
      <PageHeader
        view="deployments"
        title="Deployment details"
        action={{
          label: "Back to deployments",
          href: routePath({ view: "deployments" }),
          onClick: onBack,
          icon: <ArrowLeft size={16} />,
          tone: "quiet",
        }}
      />
      <DeploymentIdentity deployment={deployment} overview={overview} onNavigate={onOpenDeployment} />
      <nav
        className="application-sections deployment-sections"
        aria-label="Deployment details"
      >
        <DeploymentSectionLink
          id="summary"
          label="Summary"
          current={section}
          deploymentID={deployment.id}
          onSelect={onSectionChange}
        />
        <DeploymentSectionLink
          id="topology"
          label="Topology"
          current={section}
          deploymentID={deployment.id}
          onSelect={onSectionChange}
        />
        <DeploymentSectionLink
          id="values"
          label="Values"
          current={section}
          deploymentID={deployment.id}
          onSelect={onSectionChange}
        />
        <DeploymentSectionLink id="history" label="History" current={section} deploymentID={deployment.id} onSelect={onSectionChange} />
        <DeploymentSectionLink
          id="manifests"
          label="Manifests"
          current={section}
          deploymentID={deployment.id}
          onSelect={onSectionChange}
        />
      </nav>
      {section === "summary" && (
        <section
          id={deploymentEvidenceID(deployment.id)}
          className="deployment-evidence deployment-detail-surface"
          aria-labelledby={titleID}
        >
          <Evidence
            deployment={deployment}
            logs={logs}
            logsLoading={logsLoading}
            logsError={logsError}
            titleID={titleID}
            onCancel={onCancel}
            canCancel={canCancel}
          />
        </section>
      )}
      {section === "summary" && <DeploymentHealth health={deployment.health} />}
      {overview && deployment.app && !deployment.app.template && <ApplicationRoutePanel appID={deployment.appId} canConfigure={canManageProject(overview, deployment.app.projectId, "project.configure")} />}
      {overview && deployment.app && !deployment.app.template && <RuntimeSyncDisclosure key={`sync:${deployment.appId}`} application={deployment.app} overview={overview} />}
      {overview && (section === "summary" || section === "history") && <ReleaseTools deployment={deployment} canDeploy={canManageProject(overview, deployment.app?.projectId ?? "", "deployment.run")} canConfigure={canManageProject(overview, deployment.app?.projectId ?? "", "project.configure")} onDeployment={run => onOpenDeployment?.(run.id)} onSelectDeployment={onOpenDeployment ?? onSelectDeployment} />}
      {section === "history" && <DeploymentHistory key={`history:${deployment.appId}`} deployment={deployment} onSelectDeployment={onSelectDeployment} />}
      {section !== "summary" && section !== "history" && (
        <DeploymentRuntime deployment={deployment} section={section} />
      )}
    </div>
  );
}

function DeploymentSectionLink({
  id,
  label,
  current,
  deploymentID,
  onSelect,
}: {
  id: DeploymentSection;
  label: string;
  current: DeploymentSection;
  deploymentID: string;
  onSelect: (section: DeploymentSection) => void;
}) {
  return (
    <a
      className={current === id ? "active" : ""}
      aria-current={current === id ? "page" : undefined}
      href={routePath({
        view: "deployments",
        deploymentID,
        deploymentSection: id,
      })}
      onClick={(event) => {
        if (!shouldHandleNavigation(event)) return;
        event.preventDefault();
        onSelect(id);
      }}
    >
      {label}
    </a>
  );
}

export function DeploymentQuickView({
  deployment,
  logs,
  logsLoading,
  logsError,
  onClose,
  onOpenDetails,
  onCancel,
  canCancel = true,
}: {
  deployment: Deployment;
  logs: DeploymentLog[];
  logsLoading: boolean;
  logsError: string;
  onClose: () => void;
  onOpenDetails: () => void;
  onCancel: () => void;
  canCancel?: boolean;
}) {
  const titleID = `${deploymentEvidenceID(deployment.id)}-quick-title`;
  const dialogRef = useDialogFocus(onClose);
  return (
    <div
      className="dialog-layer deployment-quick-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="deployment-evidence deployment-quick-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleID}
      >
        <Evidence
          deployment={deployment}
          logs={logs}
          logsLoading={logsLoading}
          logsError={logsError}
          titleID={titleID}
          onCancel={onCancel}
          canCancel={canCancel}
          quickView
          headerActions={
            <>
              <a
                className="evidence-icon-action"
                href={routePath({
                  view: "deployments",
                  deploymentID: deployment.id,
                })}
                aria-label="Open deployment details page"
                title="Open full page"
                onClick={(event) => {
                  if (!shouldHandleNavigation(event)) return;
                  event.preventDefault();
                  onOpenDetails();
                }}
              >
                <ArrowSquareOut size={17} />
              </a>
              <button
                type="button"
                data-autofocus
                aria-label="Close deployment preview"
                title="Close"
                onClick={onClose}
              >
                <X size={18} />
              </button>
            </>
          }
        />
      </section>
    </div>
  );
}

export function MissingDeploymentPage({ onBack }: { onBack: () => void }) {
  return (
    <div className="page-layout">
      <PageHeader
        view="deployments"
        title="Deployment not found"
        action={{
          label: "Back to deployments",
          href: routePath({ view: "deployments" }),
          onClick: onBack,
          icon: <ArrowLeft size={16} />,
          tone: "quiet",
        }}
      />
      <EmptyState
        title="This deployment is unavailable"
        body="This deployment no longer exists, or the URL is incomplete."
      />
    </div>
  );
}

function deploymentEvidenceID(id: string) {
  return `deployment-evidence-${id.replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}

function Evidence({
  deployment,
  logs,
  logsLoading,
  logsError,
  titleID,
  onCancel,
  canCancel = true,
  headerActions,
  quickView = false,
}: {
  deployment: Deployment;
  logs: DeploymentLog[];
  logsLoading: boolean;
  logsError: string;
  titleID: string;
  onCancel: () => void;
  canCancel?: boolean;
  headerActions?: ReactNode;
  quickView?: boolean;
}) {
  const active = !["succeeded", "failed", "cancelled"].includes(
    deployment.state,
  );
  const [copied, setCopied] = useState("");
  const [logOpen, setLogOpen] = useState(
    active || deployment.state === "failed",
  );
  const logTitleID = `${titleID}-log`;
  async function copy(kind: string, value: string) {
    await navigator.clipboard.writeText(value);
    setCopied(kind);
    window.setTimeout(
      () => setCopied((current) => (current === kind ? "" : current)),
      1600,
    );
  }
  const logStatus = (
    <span className={logsError ? "danger" : active ? "active" : ""}>
      {logsLoading
        ? "Loading"
        : logsError
          ? "Unavailable"
          : active
            ? "Streaming"
            : `${logs.length} ${logs.length === 1 ? "entry" : "entries"}`}
    </span>
  );
  const logBody = (
    <div className="log-block">
      <div
        className="terminal"
        role="log"
        aria-live="polite"
        aria-busy={logsLoading}
      >
        {logsLoading ? (
          <p className="log-state">Loading log entries...</p>
        ) : logsError ? (
          <p className="log-state error">
            Dispatch could not load logs. Retrying automatically.
          </p>
        ) : logs.length ? (
          logs.map((entry) => (
            <div key={entry.id} className={entry.level}>
              <time>
                {new Date(entry.createdAt).toLocaleTimeString([], {
                  hour12: false,
                })}
              </time>
              <span>{entry.message}</span>
            </div>
          ))
        ) : (
          <p>
            {active
              ? "Waiting for the first log entry."
              : "This deployment has no log entries."}
          </p>
        )}
      </div>
    </div>
  );
  const logPanel = quickView ? (
    <section
      className="evidence-log evidence-log-fixed"
      aria-labelledby={logTitleID}
    >
      <div className="evidence-log-heading">
        <span id={logTitleID}>Deployment log</span>
        {logStatus}
      </div>
      {logBody}
    </section>
  ) : (
    <details
      className="evidence-log"
      open={logOpen}
      onToggle={(event) => setLogOpen(event.currentTarget.open)}
    >
      <summary>
        <span id={logTitleID}>Deployment log</span>
        {logStatus}
      </summary>
      {logBody}
    </details>
  );
  const facts = (
    <section className="evidence-facts">
      <h3>Configuration</h3>
      <dl className="evidence-grid">
        <div>
          <dt>Commit</dt>
          <dd>
            <code>{short(deployment.commitSha, 18)}</code>
            <button
              type="button"
              aria-label="Copy commit"
              onClick={() => void copy("commit", deployment.commitSha)}
            >
              {copied === "commit" ? <Check size={13} /> : <Copy size={13} />}
            </button>
          </dd>
        </div>
        <div>
          <dt>Created</dt>
          <dd>{relative(deployment.createdAt)}</dd>
        </div>
        <div className="digest">
          <dt>Spec digest</dt>
          <dd>
            <code title={deployment.specDigest}>
              {short(deployment.specDigest, 28)}
            </code>
            <button
              type="button"
              aria-label="Copy spec digest"
              onClick={() => void copy("digest", deployment.specDigest)}
            >
              {copied === "digest" ? <Check size={13} /> : <Copy size={13} />}
            </button>
          </dd>
        </div>
        <div>
          <dt>Build</dt>
          <dd>{deployment.app?.buildType}</dd>
        </div>
        <div>
          <dt>Branch</dt>
          <dd>{deployment.app?.branch}</dd>
        </div>
        <div>
          <dt>Server</dt>
          <dd>{deployment.server?.name}</dd>
        </div>
        <div>
          <dt>Runtime</dt>
          <dd>{deployment.server?.runtime}</dd>
        </div>
      </dl>
    </section>
  );
  const outputs =
    deployment.outputs && Object.keys(deployment.outputs).length > 0 ? (
      <section className="evidence-outputs">
        <h3>Published outputs</h3>
        <dl className="evidence-grid">
          {Object.entries(deployment.outputs).map(([key, value]) => (
            <div key={key}>
              <dt>{key}</dt>
              <dd>
                <code title={value}>{value}</code>
                <button
                  type="button"
                  aria-label={`Copy ${key}`}
                  onClick={() => void copy(`output-${key}`, value)}
                >
                  {copied === `output-${key}` ? (
                    <Check size={13} />
                  ) : (
                    <Copy size={13} />
                  )}
                </button>
              </dd>
            </div>
          ))}
        </dl>
      </section>
    ) : null;
  return (
    <>
      <div className="evidence-head">
        <div>
          <span className={`selection-dot ${statusTone(deployment.state)}`} />
          <div>
            <h2 id={titleID}>{deployment.app?.name}</h2>
            <small>
              <code>{short(deployment.commitSha)}</code> on{" "}
              {deployment.server?.name}
            </small>
          </div>
        </div>
        <div className="evidence-head-actions">
          <span className={`stamp ${statusTone(deployment.state)}`}>
            {deployment.state}
          </span>
          {headerActions}
        </div>
      </div>
      {quickView ? (
        <div className="evidence-content evidence-content-quick">
          <div className="evidence-metadata-scroll">
            {facts}
            {outputs}
          </div>
          {logPanel}
        </div>
      ) : (
        <div className="evidence-content">
          {logPanel}
          {facts}
          {outputs}
        </div>
      )}
      {active && canCancel && (
        <div className="evidence-actions">
          <button className="danger-button" onClick={onCancel}>
            Cancel deployment
          </button>
        </div>
      )}
    </>
  );
}
