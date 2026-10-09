import { useState, type FormEvent } from "react";
import { X } from "@phosphor-icons/react";
import {
  api,
  type AccessOverview,
  type RoleAssignment,
  type User,
} from "../api";
import { useDialogFocus } from "../useDialogFocus";

export function ProjectAccess({
  access,
  user,
  onClose,
  onSaved,
}: {
  access: AccessOverview;
  user: User;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const ref = useDialogFocus(onClose);
  const grants = access.assignments.filter(
    (grant) => grant.principalType === "user" && grant.principalId === user.id,
  );
  const [roles, setRoles] = useState<
    Record<string, RoleAssignment["role"] | "">
  >(() =>
    Object.fromEntries(grants.map((grant) => [grant.scopeId, grant.role])),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      for (const project of access.projects) {
        const prior = grants.find((grant) => grant.scopeId === project.id),
          next = roles[project.id] || "";
        if (next === (prior?.role ?? "")) continue;
        if (next)
          await api.upsertRoleAssignment({
            principalType: "user",
            principalId: user.id,
            projectId: project.id,
            role: next,
            ...(prior?.expiresAt ? { expiresAt: prior.expiresAt } : {}),
          });
        else if (prior) await api.deleteRoleAssignment(prior.id);
      }
      await onSaved();
      onClose();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="dialog-backdrop">
      <section
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="tenant-project-access-title"
        ref={ref}
      >
        <header className="dialog-header">
          <h2 id="tenant-project-access-title">
            Project access for {user.displayName}
          </h2>
          <button
            className="icon-button"
            aria-label="Close project access"
            onClick={onClose}
          >
            <X size={20} />
          </button>
        </header>
        <form onSubmit={(event) => void save(event)}>
          <div className="dialog-body hosted-form">
            {access.projects.map((project) => (
              <label key={project.id}>
                {project.name}
                <select
                  value={roles[project.id] || ""}
                  onChange={(event) =>
                    setRoles((values) => ({
                      ...values,
                      [project.id]: event.target.value as
                        | RoleAssignment["role"]
                        | "",
                    }))
                  }
                >
                  <option value="">No direct access</option>
                  {access.roles.map((role) => (
                    <option key={role.id} value={role.id}>
                      {role.name}
                    </option>
                  ))}
                </select>
              </label>
            ))}
            {access.projects.length === 0 && (
              <p>Create a project before assigning access.</p>
            )}
            {error && (
              <p className="form-error" role="alert">
                {error}
              </p>
            )}
          </div>
          <footer className="dialog-actions">
            <button
              type="button"
              className="quiet-button"
              onClick={onClose}
              disabled={busy}
            >
              Cancel
            </button>
            <button className="primary-button" disabled={busy}>
              {busy ? "Saving..." : "Save access"}
            </button>
          </footer>
        </form>
      </section>
    </div>
  );
}
