import { useState } from "react";
import { Overview } from "../api";
import { InfrastructureProviders } from "../InfrastructureProviders";
import { ManagedServers } from "../ManagedServers";
import { WorkloadBackups } from "../WorkloadBackups";
import { Backups } from "../operations/Maintenance";
import { canManageProject } from "../permissions";
import { Credentials } from "./resources/Credentials";
import { Assignments } from "./resources/Assignments";
import { Receipts } from "./resources/Receipts";
import "./ResourcesPage.css";
import { isUIFeatureEnabled, resourceUIFeature } from "../featureFlags";
import { DisabledUIFeature } from "../DisabledUIFeature";
import type { ResourceSection } from "../routes";

const titles: Record<string, string> = {
  machines: "Machines", providers: "Providers", "workload-backups": "Workload backups",
  "machine-snapshots": "Machine snapshots", "controller-backups": "Controller backups",
  credentials: "Credentials", assignments: "Assignments", receipts: "Receipts",
};

export function ResourcesPage({ overview, section, onChanged }: { overview: Overview; section: string; onChanged: () => Promise<void> }) {
  const feature = resourceUIFeature[section as ResourceSection];
  if (feature && !isUIFeatureEnabled(overview, feature)) return <div className="page-layout resources-page"><header className="page-header"><h1>{titles[section]}</h1></header><DisabledUIFeature overview={overview} feature={feature} /></div>;
  return <EnabledResourcesPage overview={overview} section={section} onChanged={onChanged} />;
}

function EnabledResourcesPage({ overview, section, onChanged }: { overview: Overview; section: string; onChanged: () => Promise<void> }) {
  const owner = overview.identity?.systemRole === "owner";
  const identityKey = JSON.stringify([overview.identity?.id, overview.identity?.systemRole, overview.projectPermissions]);
  const permitted = { ...overview, projects: overview.projects.filter(project => canManageProject(overview, project.id, "infrastructure.inspect")) };
  let content;
  if ((section === "credentials" || section === "providers" || section === "controller-backups") && !owner) {
    content = <p className="section-empty">A controller owner manages {titles[section].toLowerCase()}.</p>;
  } else if (section === "controller-backups") {
    content = overview.controllerSettings?.operationsEnabled === true
      ? <><p>Back up Dispatch's database and vault key. Application data uses workload backups.</p><Backups onChanged={() => void onChanged()} /></>
      : <p className="section-empty">Enable Operations in Settings to use controller backups.</p>;
  } else if (section === "providers") {
    content = <><p>Provider adapters supply machine allocation and snapshots. The bundled mock provider does not create real infrastructure.</p><InfrastructureProviders overview={overview} extended /></>;
  } else if (section === "machines" || section === "machine-snapshots") {
    content = permitted.projects.length
      ? <ManagedServers key={section} overview={permitted} section={section === "machines" ? "machines" : "snapshots"} enforcePermissions />
      : <p className="section-empty">You need infrastructure inspection access to view machines and snapshots.</p>;
  } else if (section === "workload-backups") {
    content = <ProjectBackups overview={overview} />;
  } else if (section === "credentials") {
    content = <Credentials overview={overview} />;
  } else if (section === "assignments") {
    content = <Assignments overview={overview} />;
  } else if (section === "receipts") {
    content = <Receipts />;
  } else {
    content = <p className="section-empty">Choose a resource tab.</p>;
  }
  return <div className="page-layout resources-page"><header className="page-header"><h1>{titles[section] || "Resources"}</h1></header><div key={`${identityKey}/${section}`} className="resources-content">{content}</div></div>;
}

function ProjectBackups({ overview }: { overview: Overview }) {
  const [project, setProject] = useState(overview.projects[0]?.id || "");
  const chosen = overview.projects.some(item => item.id === project) ? project : overview.projects[0]?.id || "";
  if (!chosen) return <p className="section-empty">No projects available.</p>;
  return <><label className="resources-project">Project<select value={chosen} onChange={event => setProject(event.target.value)}>{overview.projects.map(item => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label><WorkloadBackups key={chosen} overview={overview} project={chosen} paginated /></>;
}
