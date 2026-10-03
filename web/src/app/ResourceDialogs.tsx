import { useEffect, useRef, useState } from "react";
import { X } from "@phosphor-icons/react";
import { api, Overview, Project, Server } from "../api";
import { ProjectForm, ServerForm } from "../Onboarding";
import { useDialogFocus } from "../useDialogFocus";
import { canManageController, canManageProject } from "../permissions";
import type { Dialog, DeleteTarget } from "./dialogTypes";
import { DeployForm } from "../deployments/DeployForm";

export function canDeleteResource(overview: Overview, target: DeleteTarget) {
  if (target.kind === "server")
    return canManageController(overview, "infrastructure.manage");
  if (target.kind === "secret")
    return canManageController(overview, "secrets.manage");
  if (target.kind === "previewGroup")
    return overview.identity?.systemRole === "owner";
  return canManageProject(
    overview,
    target.kind === "project" ? target.item.id : target.item.projectId,
    target.kind === "project" ? "project.manage" : "project.configure",
  );
}

export function ResourceDialog({
  kind,
  overview,
  project,
  server,
  serverRuntime,
  deployAppID,
  onClose,
  onChanged,
  onDeployed,
}: {
  kind: Exclude<Dialog, null>;
  overview: Overview;
  project?: Project;
  server?: Server;
  serverRuntime?: Server["runtime"];
  deployAppID?: string;
  onClose: () => void;
  onChanged: () => Promise<void>;
  onDeployed: (id: string) => Promise<void>;
}) {
  const dialogRef = useDialogFocus(onClose);
  const copy = {
    server: server ? { title: "Edit server" } : { title: "Add server" },
    repair: { title: "Repair OpenShift connection" },
    project: project ? { title: "Edit project" } : { title: "Add project" },
    deploy: { title: "Deploy revision" },
  }[kind];
  return (
    <div className="dialog-layer drawer-layer">
      <section
        ref={dialogRef}
        className={`resource-dialog resource-drawer ${kind}-drawer`}
        role="dialog"
        aria-modal="true"
        aria-labelledby="dialog-title"
      >
        <header>
          <div>
            <h2 id="dialog-title">{copy.title}</h2>
          </div>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          {kind === "server" && (
            <ServerForm
              onChanged={onChanged}
              onCancel={onClose}
              server={server}
              secrets={overview.secrets}
              initialRuntime={serverRuntime}
            />
          )}
          {kind === "repair" && (
            <ServerForm
              onChanged={onChanged}
              onCancel={onClose}
              server={server}
              repairing
              secrets={overview.secrets}
            />
          )}
          {kind === "project" && (
            <ProjectForm
              onChanged={onChanged}
              onCancel={onClose}
              project={project}
            />
          )}
          {kind === "deploy" && (
            <DeployForm
              apps={overview.apps.filter(
                (app) =>
                  !app.template &&
                  canManageProject(
                    overview,
                    app.projectId,
                    "deployment.run",
                  ),
              )}
              initialAppID={deployAppID}
              onComplete={onDeployed}
              onCancel={onClose}
            />
          )}
        </div>
      </section>
    </div>
  );
}

export function DeleteDialog({ target, onClose, onDeleted }: {
  target: DeleteTarget; overview: Overview; onClose: () => void; onDeleted: () => Promise<void>;
}) {
  const started = useRef(false);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  async function remove() {
    setBusy(true); setError("");
    try {
      if (target.kind === "server") await api.deleteServer(target.item.id);
      else if (target.kind === "project") await api.deleteProject(target.item.id);
      else if (target.kind === "secret") await api.deleteSecret(target.item.id);
      else if (target.kind === "previewGroup") await api.deletePreviewGroup(target.item.id);
      else await api.deleteApp(target.item.id);
      await onDeleted();
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause);
      if (!message) onClose(); else setError(message);
    } finally { setBusy(false); }
  }
  useEffect(() => { if (!started.current) { started.current = true; void remove(); } }, []);
  return <div className="dialog-layer confirm-layer"><section className="resource-dialog confirm-dialog" role="dialog" aria-modal="true" aria-label={`Delete ${target.item.name}`}>
    <header><h2>Delete {target.item.name}</h2><button aria-label="Close dialog" disabled={busy} onClick={onClose}><X size={19} /></button></header>
    <div className="dialog-body">{busy ? <p role="status">Reviewing or removing this resource...</p> : <><p role="alert" className="form-error">{error}</p><div className="dialog-actions"><button className="quiet-button" onClick={onClose}>Close</button><button className="danger-button" onClick={() => void remove()}>Review again</button></div></>}</div>
  </section></div>;
}
