import { useEffect, useId, useState, type KeyboardEvent, type ReactNode } from "react";
import { ArrowClockwise, ArrowRight, CheckCircle, Clock, ClockCounterClockwise, Database, HardDrives, Info, ListChecks, ShieldCheck, Trash, UsersThree, WarningCircle } from "@phosphor-icons/react";
import { type Overview } from "./api";
import { canManageProject } from "./permissions";
import { relative } from "./presentation";
import { type AppRoute, type OperationsFilters, type OperationsSection } from "./routes";
import { Activity, auditActionLabel } from "./operations/Activity";
import { Ownership } from "./operations/Ownership";
import { IdentityMappings } from "./operations/IdentityMappings";
import { Backups, RetentionControls } from "./operations/Maintenance";
import { operationsClient, type OperationsSummary } from "./operations/client";
import "./OperationsPage.css";

type Props = { overview: Overview; filters?: OperationsFilters; onFilters?: (filters: OperationsFilters) => void; onNavigate?: (route: AppRoute) => void; onChanged?: () => void | Promise<void> };
const sections = [
  { id: "overview", label: "Overview", Icon: ListChecks },
  { id: "activity", label: "Activity", Icon: ClockCounterClockwise },
  { id: "ownership", label: "Ownership", Icon: UsersThree },
  { id: "retention", label: "Cleanup", Icon: Trash },
  { id: "backups", label: "Backups", Icon: HardDrives, owner: true },
  { id: "identity", label: "Access mappings", Icon: ShieldCheck, owner: true },
] as const;

export function OperationsPage({ overview, filters, onFilters, onNavigate, onChanged }: Props) {
  const [local, setLocal] = useState<OperationsFilters>(filters ?? {});
  const [refresh, setRefresh] = useState(0);
  const selected = onFilters ? filters ?? {} : local;
  const owner = overview.identity?.systemRole === "owner";
  const tabs = sections.filter(section => !("owner" in section) || owner);
  const section = tabs.some(tab => tab.id === selected.section) ? selected.section! : "overview";
  const controllerSection = section === "backups" || section === "identity";
  const tabID = useId();
  const change = (next: OperationsFilters) => onFilters ? onFilters(next) : setLocal(next);
  const open = (next: OperationsSection, patch: Partial<OperationsFilters> = {}) => change({ section: next, projectId: selected.projectId, ...patch });
  const changed = () => { setRefresh(value => value + 1); void onChanged?.(); };
  const tabKey = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const next = event.key === "ArrowRight" ? (index + 1) % tabs.length : event.key === "ArrowLeft" ? (index + tabs.length - 1) % tabs.length : event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : -1;
    if (next < 0) return;
    event.preventDefault(); open(tabs[next].id);
    document.getElementById(`${tabID}-${tabs[next].id}`)?.focus();
  };

  return <div className="page-layout operations-page">
    <header className="page-header operations-header"><div><h1>Operations</h1><p>Review changes, assign responsibility, and prepare for recovery.</p></div></header>
    <div className="operations-navigation">
      <div className="operations-tabs" role="tablist" aria-label="Operations">{tabs.map(({id, label, Icon}, index) => <button type="button" key={id} id={`${tabID}-${id}`} role="tab" aria-selected={section === id} aria-controls={`${tabID}-panel`} tabIndex={section === id ? 0 : -1} onClick={() => open(id)} onKeyDown={event => tabKey(event, index)}><Icon size={15} />{label}</button>)}</div>
      {controllerSection ? <span className="operations-controller-scope"><ShieldCheck size={14} />Controller-wide</span> : <label className="operations-project" htmlFor={`${tabID}-project`}>Project<select id={`${tabID}-project`} aria-label="Operations project" value={selected.projectId ?? ""} onChange={event => change({...selected, projectId: event.target.value || undefined, appId: undefined})}><option value="">All visible projects</option>{overview.projects.map(project => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>}
    </div>
    <div className="operations-section" id={`${tabID}-panel`} role="tabpanel" aria-labelledby={`${tabID}-${section}`}>
      {section === "overview" && <OperationsOverview overview={overview} projectId={selected.projectId} refresh={refresh} onRefresh={() => setRefresh(value => value + 1)} open={open} />}
      {section === "activity" && <Activity overview={overview} filters={selected} onFilters={change} onNavigate={onNavigate} />}
      {section === "ownership" && <Ownership overview={overview} filters={selected} onFilters={change} onChanged={changed} onNavigate={onNavigate} />}
      {section === "retention" && <RetentionControls overview={overview} projectId={selected.projectId} onChanged={changed} />}
      {section === "backups" && owner && <Backups onChanged={changed} />}
      {section === "identity" && owner && <IdentityMappings onChanged={changed} />}
    </div>
  </div>;
}

function OperationsOverview({ overview, projectId, refresh, onRefresh, open }: { overview: Overview; projectId?: string; refresh: number; onRefresh: () => void; open: (section: OperationsSection, patch?: Partial<OperationsFilters>) => void }) {
  const [data, setData] = useState<OperationsSummary>();
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const owner = overview.identity?.systemRole === "owner";
  const grants = JSON.stringify(overview.projectPermissions);
  useEffect(() => {
    let current = true;
    setData(undefined); setError(""); setLoading(true);
    operationsClient.summary(projectId).then(value => { if (current) setData(value); })
      .catch(cause => { if (current) setError(cause instanceof Error ? cause.message : "Operations could not be loaded."); })
      .finally(() => { if (current) setLoading(false); });
    return () => { current = false; };
  }, [projectId, refresh, overview.identity?.id, owner, grants]);
  const canAssign = overview.projects.some(project => (!projectId || project.id === projectId) && canManageProject(overview, project.id, "project.configure"));
  const canClean = overview.projects.some(project => (!projectId || project.id === projectId) && canManageProject(overview, project.id, "project.manage"));
  const backup = owner ? data?.backups : undefined;
  const latest = backup?.latest;
  const backupFailed = latest?.state === "failed" || latest?.state === "verification_failed";
  const backupVerified = latest?.state === "verified" && Boolean(latest.verifiedAt) && Number.isFinite(Date.parse(latest.verifiedAt!));
  const backupAttention = backup && (!backup.configured || !latest || !backupVerified);
  const attention = Boolean(data && (data.audit.rejected || data.ownership.unassigned || backupAttention));
  const inspectRejections = () => open("activity", data ? {outcome: "rejected", since: data.audit.since, until: data.observedAt} : {});

  return <>
    <div className="operations-overview-heading"><span>{data ? <>Observed {relative(data.observedAt)}<span className="operations-observation-note"> · Saved controller records</span></> : "Saved controller records"}</span><button type="button" className="quiet-button" disabled={loading} onClick={onRefresh}><ArrowClockwise size={14} />{loading ? "Loading…" : "Refresh"}</button></div>
    {loading && <div className="operations-empty" role="status"><Clock size={22} /><strong>Loading operations</strong><span>Checking recent activity and ownership.</span></div>}
    {error && <div className="operations-empty operations-load-error" role="alert"><WarningCircle size={22} /><strong>Operations could not be loaded</strong><span>{error}</span><button type="button" className="quiet-button" onClick={onRefresh}>Try again</button></div>}
    {data && <>
      <dl className={`operations-summary-strip${backup ? " with-backup" : ""}`}>
        <SummaryMetric label="Actions in 24 hours" value={number(data.audit.total)} detail={`${number(data.audit.total - data.audit.rejected)} accepted requests`} icon={<ClockCounterClockwise size={15} />} />
        <SummaryMetric label="Rejected requests" value={number(data.audit.rejected)} detail="Includes permission and validation errors" icon={<WarningCircle size={15} />} tone={data.audit.rejected ? "warning" : undefined} />
        <SummaryMetric label="Ownership" value={`${number(data.ownership.total - data.ownership.unassigned)} / ${number(data.ownership.total)}`} detail="Applications with an assigned contact" icon={<UsersThree size={15} />} />
        {backup && <SummaryMetric label="Latest controller backup" value={!backup.configured ? "Unavailable" : !latest ? "None recorded" : backupFailed ? "Needs review" : latest.state === "creating" ? "Incomplete" : backupVerified ? "Verified" : "Not verified"} detail={latest ? `Created ${relative(latest.createdAt)}` : "Database and vault key"} icon={<HardDrives size={15} />} tone={backupVerified ? "success" : undefined} />}
      </dl>
      <div className="operations-overview-grid">
        <section className="operations-overview-panel operations-attention"><header><div><h2>Review next</h2><p>Requests, ownership, and recovery checks that need attention.</p></div><ListChecks size={18} /></header>
          {attention ? <div className="operations-task-list">
            {data.audit.rejected > 0 && <TaskRow icon={<WarningCircle size={19} />} title={`${number(data.audit.rejected)} ${data.audit.rejected === 1 ? "request was" : "requests were"} rejected`} detail="See which requests were blocked and who made them." action="Review activity" onClick={inspectRejections} tone="warning" />}
            {data.ownership.unassigned > 0 && <TaskRow icon={<UsersThree size={19} />} title={`${number(data.ownership.unassigned)} ${data.ownership.unassigned === 1 ? "application has" : "applications have"} no owner`} detail="Give teammates a person or team to contact for each application." action={canAssign ? "Assign owners" : "View ownership"} onClick={() => open("ownership", {unassigned: true})} />}
            {backupAttention && <TaskRow icon={<HardDrives size={19} />} title={!backup!.configured ? "Controller backups are unavailable" : !latest ? "No controller backup recorded" : backupFailed ? "The latest backup needs review" : latest.state === "creating" ? "The latest backup has not completed" : "The latest backup has not been verified"} detail={!backup!.configured ? "Review the backup configuration to prepare a recovery copy." : !latest ? "Create a database and vault-key backup, then check that it can be restored." : backupFailed ? "Inspect the recorded failure and choose a usable recovery copy." : latest.state === "creating" ? "Review the saved attempt and create a new copy if needed." : "Run a restore check in a disposable database before relying on this copy."} action={!latest ? "Open backups" : "Review backup"} onClick={() => open("backups")} tone={backupFailed ? "warning" : undefined} />}
          </div> : <div className="operations-clear"><CheckCircle size={22} /><strong>No follow-up items in these records</strong><span>Review activity or use a maintenance tool below. Runtime health is shown on Deployments.</span></div>}
        </section>
        <aside className="operations-overview-panel operations-routine"><header><div><h2>Routine maintenance</h2><p>Run these when needed.</p></div></header>
          <RoutineAction icon={<ClockCounterClockwise size={18} />} title="Trace a change" detail="Find the actor, application, and request outcome." onClick={() => open("activity")} />
          {canClean && <RoutineAction icon={<Trash size={18} />} title="Review history cleanup" detail="Preview eligible logs and failed runs before removal." onClick={() => open("retention")} />}
          {owner && <RoutineAction icon={<Database size={18} />} title="Check recovery readiness" detail="Create a controller backup or verify a saved copy." onClick={() => open("backups")} />}
          {!canClean && !owner && <RoutineAction icon={<UsersThree size={18} />} title="Find an application owner" detail="See who is responsible for an environment." onClick={() => open("ownership")} />}
        </aside>
      </div>
      <section className="operations-overview-panel operations-recent"><header><div><h2>Recent activity</h2><p>Latest recorded requests within the last 24 hours</p></div><button type="button" className="operations-text-action" onClick={() => open("activity")}>View activity<ArrowRight size={14} /></button></header>
        {data.audit.recent.length ? <ol>{data.audit.recent.map(event => <li key={event.id}><span className={`operations-event-icon ${event.outcome === "rejected" ? "warning" : "success"}`}>{event.outcome === "rejected" ? <WarningCircle size={16} /> : <CheckCircle size={16} />}</span><div><strong>{auditActionLabel(event.action)}</strong><span>{event.actorName || event.actorId || "Recorded actor"}{event.projectId && ` · ${overview.projects.find(project => project.id === event.projectId)?.name || "Project"}`}</span></div><span className="operations-event-outcome">{event.outcome === "rejected" ? "Rejected" : "Accepted"}</span><time dateTime={event.createdAt} title={new Date(event.createdAt).toLocaleString()}>{relative(event.createdAt)}</time></li>)}</ol> : <p className="operations-recent-empty">No requests recorded in this period.</p>}
      </section>
      {backup && <p className="operations-scope-note"><Info size={14} />Backup information is controller-wide. Application data, volumes, and analytics archives require their own backup plan.</p>}
    </>}
  </>;
}

function number(value: number) { return new Intl.NumberFormat("en").format(value); }
function SummaryMetric({label, value, detail, icon, tone}: {label: string; value: string; detail: string; icon: ReactNode; tone?: string}) {
  return <div className={`operations-summary-metric ${tone ?? ""}`}><dt>{icon}{label}</dt><dd>{value}</dd><small>{detail}</small></div>;
}
function TaskRow({icon, title, detail, action, onClick, tone}: {icon: ReactNode; title: string; detail: string; action: string; onClick: () => void; tone?: string}) {
  return <div className={`operations-task ${tone ?? ""}`}><span className="operations-task-icon">{icon}</span><div><h3>{title}</h3><p>{detail}</p></div><button type="button" className="quiet-button" onClick={onClick}>{action}<ArrowRight size={13} /></button></div>;
}
function RoutineAction({icon, title, detail, onClick}: {icon: ReactNode; title: string; detail: string; onClick: () => void}) {
  return <button type="button" className="operations-routine-action" onClick={onClick}>{icon}<span><strong>{title}</strong><small>{detail}</small></span><ArrowRight size={15} /></button>;
}
