import { useCallback, useEffect, useState, type FormEvent } from "react";
import { api, request, type User } from "./api";
import { useSetupRefresh } from "./hosted/useSetupRefresh";

export type LocalInstanceConnection = {
  configured: boolean;
  connected: boolean;
  controlPlaneURL: string;
  instanceID: string;
  installationID: string;
  directURL: string;
  remoteAccess: boolean;
  userMappings: { subject: string; userID: string }[];
  lastError?: string;
};

export function InstanceConnectionSettings() {
  const [expanded, setExpanded] = useState(false);
  return (
    <section className="settings-features instance-connection-settings">
      <header>
        <div>
          <h2>Doodship connection</h2>
          <p>
            Receive webhooks and choose whether members can access this
            installation remotely.
          </p>
        </div>
        <button
          className="quiet-button"
          aria-expanded={expanded}
          onClick={() => setExpanded((value) => !value)}
        >
          {expanded ? "Hide connection" : "Manage connection"}
        </button>
      </header>
      {expanded && <ConnectionForm />}
    </section>
  );
}

function ConnectionForm() {
  const [status, setStatus] = useState<LocalInstanceConnection>();
  const [users, setUsers] = useState<User[]>([]);
  const [controlPlaneURL, setControlPlaneURL] = useState("");
  const [enrollmentToken, setEnrollmentToken] = useState("");
  const [directURL, setDirectURL] = useState("");
  const [remoteAccess, setRemoteAccess] = useState(false);
  const [mappings, setMappings] = useState<
    LocalInstanceConnection["userMappings"]
  >([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const apply = (value: LocalInstanceConnection) => {
    setStatus(value);
    setControlPlaneURL(value.controlPlaneURL ?? "");
    setDirectURL(value.directURL ?? "");
    setRemoteAccess(value.remoteAccess === true);
    setMappings(value.userMappings ?? []);
  };
  const load = useCallback(async () => {
    setError("");
    try {
      const [connection, access] = await Promise.all([
        request<LocalInstanceConnection>("/api/v1/instance-connection"),
        api.access(),
      ]);
      apply(connection);
      setUsers(access.users.filter((user) => user.state === "active"));
    } catch (cause) {
      setError((cause as Error).message);
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  const refreshStatus = useCallback(async () => {
    try {
      const value = await request<LocalInstanceConnection>(
        "/api/v1/instance-connection",
      );
      // Keep connection state live without overwriting unsaved form edits.
      setStatus(value);
    } catch {
      setStatus((value) => (value ? { ...value, connected: false } : value));
    }
  }, []);
  useSetupRefresh(status?.configured === true && !busy, refreshStatus);
  const save = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const saved = await request<LocalInstanceConnection>(
        "/api/v1/instance-connection",
        {
          method: "PUT",
          body: JSON.stringify({
            controlPlaneURL: controlPlaneURL.trim(),
            ...(enrollmentToken.trim()
              ? { enrollmentToken: enrollmentToken.trim() }
              : {}),
            directURL: directURL.trim(),
            remoteAccess,
            userMappings: mappings.map((mapping) => ({
              subject: mapping.subject.trim(),
              userID: mapping.userID,
            })),
          }),
        },
      );
      apply(saved);
      setEnrollmentToken("");
      setNotice(
        "Connection saved. This installation will connect to Doodship.",
      );
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await request("/api/v1/instance-connection", { method: "DELETE" });
      setConfirmDisconnect(false);
      setEnrollmentToken("");
      await load();
      setNotice("Disconnected. Local accounts and jobs remain available.");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="instance-connection-body">
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {!status && !error && <p role="status">Loading connection...</p>}
      {status && (
        <form onSubmit={(event) => void save(event)}>
          <p role="status">
            {status.configured
              ? status.connected
                ? "Connected"
                : "Waiting for connection"
              : "Not connected"}
          </p>
          {status.lastError && <p role="alert">{status.lastError}</p>}
          <fieldset disabled={busy}>
            <label>
              Control plane address
              <input
                type="url"
                required
                placeholder="https://doodship.com"
                value={controlPlaneURL}
                onChange={(event) => setControlPlaneURL(event.target.value)}
                readOnly={status.configured}
              />
            </label>
            {!status.configured && (
              <label>
                Connection token
                <input
                  type="password"
                  required
                  autoComplete="off"
                  value={enrollmentToken}
                  onChange={(event) => setEnrollmentToken(event.target.value)}
                />
              </label>
            )}
            <label>
              Instance address
              <input
                type="url"
                placeholder="https://doodship.example.com"
                value={directURL}
                onChange={(event) => setDirectURL(event.target.value)}
              />
            </label>
            <p>
              Members use this address when remote access is disabled. It may
              require their LAN or VPN.
            </p>
            <label className="instance-connection-toggle">
              <input
                type="checkbox"
                checked={remoteAccess}
                onChange={(event) => setRemoteAccess(event.target.checked)}
              />
              Allow remote user access
            </label>
            <p>
              Webhooks can be forwarded without remote user access. All
              connections start from this installation.
            </p>
            <h3>Linked users</h3>
            <p>
              Map each Doodship account ID to a local user. Their local
              permissions apply.
            </p>
            {mappings.map((mapping, index) => (
              <div className="instance-connection-mapping" key={index}>
                <label>
                  Doodship account ID {index + 1}
                  <input
                    required
                    value={mapping.subject}
                    autoComplete="off"
                    onChange={(event) =>
                      setMappings((values) =>
                        values.map((value, position) =>
                          position === index
                            ? { ...value, subject: event.target.value }
                            : value,
                        ),
                      )
                    }
                  />
                </label>
                <label>
                  Local user {index + 1}
                  <select
                    required
                    value={mapping.userID}
                    onChange={(event) =>
                      setMappings((values) =>
                        values.map((value, position) =>
                          position === index
                            ? { ...value, userID: event.target.value }
                            : value,
                        ),
                      )
                    }
                  >
                    <option value="">Choose a user</option>
                    {users.map((user) => (
                      <option key={user.id} value={user.id}>
                        {user.displayName} ({user.username})
                      </option>
                    ))}
                  </select>
                </label>
                <button
                  className="quiet-button"
                  type="button"
                  aria-label={`Remove linked user ${index + 1}`}
                  onClick={() =>
                    setMappings((values) =>
                      values.filter((_, position) => position !== index),
                    )
                  }
                >
                  Remove
                </button>
              </div>
            ))}
            <button
              type="button"
              className="quiet-button"
              onClick={() =>
                setMappings((values) => [
                  ...values,
                  { subject: "", userID: "" },
                ])
              }
            >
              Link a user
            </button>
            <div className="instance-connection-actions">
              <button className="primary-button" type="submit">
                {busy
                  ? "Saving..."
                  : status.configured
                    ? "Save connection"
                    : "Connect installation"}
              </button>
              <button
                className="quiet-button"
                type="button"
                onClick={() => void load()}
              >
                Refresh connection
              </button>
              {status.configured && (
                <button
                  type="button"
                  className="quiet-button"
                  onClick={() => setConfirmDisconnect(true)}
                >
                  Disconnect
                </button>
              )}
            </div>
          </fieldset>
        </form>
      )}
      {!status && error && (
        <button className="quiet-button" onClick={() => void load()}>
          Retry connection
        </button>
      )}
      {confirmDisconnect && (
        <div className="instance-connection-confirm">
          <h3>Disconnect from Doodship?</h3>
          <p>
            Remote sessions and webhook forwarding stop. Your local accounts,
            data, and jobs stay available.
          </p>
          <button
            className="primary-button"
            disabled={busy}
            onClick={() => void disconnect()}
          >
            Confirm disconnect
          </button>
          <button
            className="quiet-button"
            disabled={busy}
            onClick={() => setConfirmDisconnect(false)}
          >
            Cancel
          </button>
        </div>
      )}
    </div>
  );
}
