import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  hostedRequest,
  type Account,
  type Membership,
  type TenantMember,
} from "./client";

export function TenantMembers({
  membership,
  account,
  onClose,
  onChanged,
}: {
  membership: Membership;
  account: Account;
  onClose: () => void;
  onChanged: () => Promise<void>;
}) {
  const [members, setMembers] = useState<TenantMember[] | null>(null),
    [error, setError] = useState(""),
    [email, setEmail] = useState(""),
    [role, setRole] = useState("member"),
    [busy, setBusy] = useState(false),
    [remove, setRemove] = useState<TenantMember | null>(null);
  const path = `/tenants/${encodeURIComponent(membership.tenant.id)}/members`;
  const canManage = membership.role === "owner";
  const load = useCallback(async () => {
    setError("");
    try {
      setMembers(await hostedRequest<TenantMember[]>(path));
    } catch (cause) {
      setError((cause as Error).message);
    }
  }, [path]);
  useEffect(() => {
    setMembers(null);
    void load();
  }, [load]);
  const mutate = async (
    action: () => Promise<unknown>,
    removedSelf = false,
  ) => {
    setBusy(true);
    setError("");
    try {
      await action();
      await onChanged();
      if (removedSelf) {
        onClose();
        return;
      }
      await load();
      setRemove(null);
      setEmail("");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const add = (event: FormEvent) => {
    event.preventDefault();
    void mutate(() => hostedRequest(path, "POST", { email, role }));
  };
  const owners =
    members?.filter(
      (member) => member.role === "owner" && member.state === "active",
    ).length ?? 0;
  return (
    <section
      className="hosted-panel"
      aria-label={`${membership.tenant.name} members`}
    >
      <header className="hosted-panel-header">
        <div>
          <h2>{membership.tenant.name} members</h2>
          <p>
            {canManage
              ? "Owners can add members and change their roles."
              : "Only tenant owners can change membership."}
          </p>
        </div>
        <button className="quiet-button" onClick={onClose}>
          Close
        </button>
      </header>
      {error && (
        <p role="alert" className="form-error">
          {error}{" "}
          <button className="quiet-button" onClick={() => void load()}>
            Retry
          </button>
        </p>
      )}
      {!members && !error && <p role="status">Loading members...</p>}
      {members && (
        <div className="hosted-table-wrap">
          <table className="hosted-table">
            <thead>
              <tr>
                <th>Member</th>
                <th>Role</th>
                <th>Status</th>
                {canManage && (
                  <th>
                    <span className="sr-only">Actions</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {members.map((member) => {
                const lastOwner =
                  member.role === "owner" &&
                  member.state === "active" &&
                  owners === 1;
                return (
                  <tr key={member.userId}>
                    <td>
                      <strong>{member.user.name}</strong>
                      <small>
                        {member.user.email}
                        {member.userId === account.id ? " · You" : ""}
                      </small>
                    </td>
                    <td>
                      {canManage ? (
                        <select
                          aria-label={`Role for ${member.user.name}`}
                          value={member.role}
                          disabled={busy || lastOwner}
                          onChange={(event) =>
                            void mutate(
                              () =>
                                hostedRequest(
                                  `${path}/${encodeURIComponent(member.userId)}`,
                                  "PUT",
                                  {
                                    role: event.target.value,
                                    state: member.state,
                                  },
                                ),
                              member.userId === account.id &&
                                event.target.value !== "owner",
                            )
                          }
                        >
                          <option value="owner">Owner</option>
                          <option value="admin">Admin</option>
                          <option value="member">Member</option>
                        </select>
                      ) : (
                        member.role
                      )}
                    </td>
                    <td>
                      {member.state}
                      {lastOwner && <small>Last owner</small>}
                    </td>
                    {canManage && (
                      <td>
                        <button
                          className="quiet-button"
                          disabled={
                            busy || lastOwner || member.state !== "active"
                          }
                          onClick={() => setRemove(member)}
                        >
                          Remove
                        </button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {remove && (
        <div className="hosted-confirm" role="alert">
          <p>
            Remove {remove.user.name} from {membership.tenant.name}? Their
            tenant access will end immediately.
          </p>
          <div className="hosted-row-actions">
            <button
              className="danger-button"
              disabled={busy}
              onClick={() =>
                void mutate(
                  () =>
                    hostedRequest(
                      `${path}/${encodeURIComponent(remove.userId)}`,
                      "DELETE",
                    ),
                  remove.userId === account.id,
                )
              }
            >
              Remove member
            </button>
            <button
              className="quiet-button"
              disabled={busy}
              onClick={() => setRemove(null)}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
      {canManage && (
        <form className="hosted-form hosted-add-member" onSubmit={add}>
          <h3>Add member</h3>
          <label>
            Email
            <input
              type="email"
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              required
            />
          </label>
          <p>This person must have a verified Dispatch account.</p>
          <label>
            Role
            <select
              value={role}
              onChange={(event) => setRole(event.target.value)}
            >
              <option value="member">Member</option>
              <option value="admin">Admin</option>
              <option value="owner">Owner</option>
            </select>
          </label>
          <button className="primary-button" disabled={busy}>
            {busy ? "Saving..." : "Add member"}
          </button>
        </form>
      )}
    </section>
  );
}
