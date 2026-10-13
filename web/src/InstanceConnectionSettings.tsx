import { useCallback, useEffect, useId, useState, type FormEvent } from "react";
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

export function InstanceConnectionSettings({
  initialExpanded = false,
  active = true,
}: {
  initialExpanded?: boolean;
  active?: boolean;
}) {
  const [expanded, setExpanded] = useState(initialExpanded);
  const [visited, setVisited] = useState(initialExpanded);
  const id = useId();
  return (
    <section
      className="settings-features instance-connection-settings"
      data-expanded={expanded}
      hidden={!active}
    >
      <header>
        <div>
          <h2>Doodship</h2>
          <p>
            Connect this installation for webhooks and optional remote access.
          </p>
        </div>
        {!initialExpanded && (
          <button
            type="button"
            className="quiet-button"
            aria-controls={id}
            aria-expanded={expanded}
            onClick={() => {
              setVisited(true);
              setExpanded((value) => !value);
            }}
          >
            {expanded ? "Hide connection" : "Manage connection"}
          </button>
        )}
      </header>
      {visited && (
        <div hidden={!expanded}>
          <ConnectionForm id={id} active={active && expanded} />
        </div>
      )}
    </section>
  );
}

function ConnectionForm({ id, active }: { id: string; active: boolean }) {
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
  useSetupRefresh(
    active && status?.configured === true && !busy,
    refreshStatus,
  );
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
    <div className="instance-connection-body" id={id}>
      {error && (
        <p className="settings-feedback settings-error" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p className="settings-feedback settings-success" role="status">
          {notice}
        </p>
      )}
      {!status && !error && <p role="status">Loading connection...</p>}
      {status && (
        <form onSubmit={(event) => void save(event)}>
          <div
            className="instance-connection-status"
            data-state={status.connected ? "connected" : "disconnected"}
          >
            <span className="instance-connection-status-label" role="status">
              {status.configured
                ? status.connected
                  ? "Connected"
                  : "Waiting for connection"
                : "Not connected"}
            </span>
            <p>
              {status.configured
                ? "This installation opens the connection to Doodship."
                : "Create a connection token in Doodship to get started."}
            </p>
          </div>
          {status.lastError && (
            <p className="settings-error" role="alert">
              {status.lastError}
            </p>
          )}
          <fieldset disabled={busy}>
            <section
              className="instance-connection-section"
              aria-labelledby={`${id}-setup`}
            >
              <div className="instance-connection-section-heading">
                <h3 id={`${id}-setup`}>Connection</h3>
                <p>The Doodship address and token for this installation.</p>
              </div>
              <div className="instance-connection-fields">
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
                  <div className="settings-field">
                    <label htmlFor={`${id}-token`}>
                      Connection token
                      <input
                        id={`${id}-token`}
                        type="password"
                        required
                        autoComplete="off"
                        value={enrollmentToken}
                        onChange={(event) =>
                          setEnrollmentToken(event.target.value)
                        }
                        aria-describedby={`${id}-token-help`}
                      />
                    </label>
                    <p className="settings-field-help" id={`${id}-token-help`}>
                      Use the single-use token from your Doodship instance.
                    </p>
                  </div>
                )}
              </div>
            </section>
            <section
              className="instance-connection-section"
              aria-labelledby={`${id}-access`}
            >
              <div className="instance-connection-section-heading">
                <h3 id={`${id}-access`}>Member access</h3>
                <p>Choose how members open this installation.</p>
              </div>
              <div className="instance-connection-fields">
                <div className="settings-field">
                  <label htmlFor={`${id}-address`}>
                    Instance address
                    <input
                      id={`${id}-address`}
                      type="url"
                      placeholder="https://dispatch.example.com"
                      value={directURL}
                      onChange={(event) => setDirectURL(event.target.value)}
                      aria-describedby={`${id}-address-help`}
                    />
                  </label>
                  <p className="settings-field-help" id={`${id}-address-help`}>
                    Used when remote access is off. Members may need a LAN or
                    VPN connection.
                  </p>
                </div>
                <div className="instance-connection-remote">
                  <div>
                    <label htmlFor={`${id}-remote`}>
                      Allow remote user access
                    </label>
                    <p id={`${id}-remote-help`}>
                      Members can open this installation through Doodship.
                      Webhook forwarding works with this turned off.
                    </p>
                  </div>
                  <label className="settings-switch">
                    <input
                      id={`${id}-remote`}
                      type="checkbox"
                      role="switch"
                      aria-describedby={`${id}-remote-help`}
                      checked={remoteAccess}
                      onChange={(event) =>
                        setRemoteAccess(event.target.checked)
                      }
                    />
                    <span aria-hidden="true" />
                  </label>
                </div>
              </div>
            </section>
            <section
              className="instance-connection-section"
              aria-labelledby={`${id}-users`}
            >
              <div className="instance-connection-section-heading">
                <h3 id={`${id}-users`}>Linked users</h3>
                <p>
                  Link Doodship accounts to local users. Local permissions
                  apply.
                </p>
              </div>
              <div className="instance-connection-fields">
                {mappings.length === 0 && (
                  <p className="instance-connection-empty">No users linked.</p>
                )}
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
                  className="quiet-button instance-connection-link"
                  onClick={() =>
                    setMappings((values) => [
                      ...values,
                      { subject: "", userID: "" },
                    ])
                  }
                >
                  Link a user
                </button>
              </div>
            </section>
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
                  className="quiet-button instance-connection-disconnect"
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
        <button
          type="button"
          className="quiet-button"
          onClick={() => void load()}
        >
          Retry connection
        </button>
      )}
      {confirmDisconnect && (
        <div
          className="instance-connection-confirm"
          role="group"
          aria-labelledby={`${id}-disconnect`}
        >
          <h3 id={`${id}-disconnect`}>Disconnect from Doodship?</h3>
          <p>
            Remote sessions and webhook forwarding stop. Local accounts, data,
            and jobs stay available.
          </p>
          <div className="instance-connection-actions">
            <button
              type="button"
              className="danger-button"
              disabled={busy}
              onClick={() => void disconnect()}
            >
              Confirm disconnect
            </button>
            <button
              type="button"
              className="quiet-button"
              disabled={busy}
              onClick={() => setConfirmDisconnect(false)}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
