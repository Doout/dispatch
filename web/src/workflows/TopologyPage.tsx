import { useEffect, useState } from "react";
import { ArrowLeft, RocketLaunch, WarningCircle } from "@phosphor-icons/react";
import { api, ConfigSource, WorkflowResource, WorkflowTopology } from "../api";
import { PageHeader } from "../PageHeader";
import { TopologyCanvas } from "./TopologyCanvas";

export function WorkflowTopologyPage({ resource, source, onBack, onRun }: { resource: WorkflowResource; source?: ConfigSource; onBack: () => void; onRun: () => Promise<void> }) {
  const [topology, setTopology] = useState<WorkflowTopology | null>(null);
  const [error, setError] = useState("");
  const [running, setRunning] = useState(false);

  useEffect(() => {
    let active = true;
    setTopology(null);
    setError("");
    void api.workflowTopology(resource.id).then((next) => active && setTopology(next)).catch((cause: Error) => active && setError(cause.message));
    return () => { active = false; };
  }, [resource.id]);

  async function run() {
    setRunning(true);
    setError("");
    try { await onRun(); } catch (cause) { setError((cause as Error).message); } finally { setRunning(false); }
  }

  return <div className="page-layout topology-page">
    <PageHeader view="applications" title={resource.name} action={{ label: "Back", onClick: onBack, icon: <ArrowLeft size={16} />, tone: "quiet" }} />
    <div className="topology-context">
      <div><span className={`status-label ${resource.active ? resource.state : "paused"}`}><i />{resource.active ? resource.state : "paused"}</span><span>{source?.name ?? "Repository configuration"}</span><code>{resource.path}</code></div>
      {resource.active && resource.kind === "Application" && <button className="primary-button" disabled={running} onClick={() => void run()}><RocketLaunch size={15} />{running ? "Starting" : "Run"}</button>}
    </div>
    {error && <p className="topology-error" role="alert"><WarningCircle size={16} weight="fill" />{error}</p>}
    {!error && !topology && <div className="topology-loading"><span /><span /><span /></div>}
    {topology && <TopologyCanvas topology={topology} />}
  </div>;
}
