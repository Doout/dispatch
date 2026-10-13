import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, ArrowsClockwise } from "@phosphor-icons/react";
import {
  hostedRequest,
  type InstanceConnectionStatus,
  type Membership,
} from "./client";
import { useSetupRefresh } from "./useSetupRefresh";

type Enrollment = {
  enrollmentToken: string;
  expiresAt: string;
  platformURL: string;
  subject: string;
};

export function InstanceConnection({
  membership,
  onClose,
  onChanged,
}: {
  membership: Membership;
  onClose: () => void;
  onChanged: () => Promise<void>;
}) {
  const [status, setStatus] = useState<InstanceConnectionStatus>();
  const [enrollment, setEnrollment] = useState<Enrollment>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmDisconnect, setConfirmDisconnect] = useState(false);
  const path = `/tenants/${encodeURIComponent(membership.tenant.id)}/connection`;
  const refresh = useCallback(async () => {
    try {
      setStatus(await hostedRequest<InstanceConnectionStatus>(path));
      setError("");
    } catch (cause) {
      setError((cause as Error).message);
    }
  }, [path]);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  useSetupRefresh(!busy, refresh);
  const createEnrollment = async () => {
    setBusy(true);
    setError("");
    setEnrollment(undefined);
    try {
      setEnrollment(await hostedRequest<Enrollment>(path, "POST", {}));
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    setBusy(true);
    setError("");
    try {
      await hostedRequest(path, "DELETE");
      setEnrollment(undefined);
      setConfirmDisconnect(false);
      await refresh();
      await onChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="hosted-connection-page">
      <button className="quiet-button" onClick={onClose}>
        <ArrowLeft size={16} aria-hidden="true" />
        Back to instances
      </button>
      <header className="hosted-page-heading">
        <div>
          <h1>Connect {membership.tenant.name}</h1>
          <p>Your server stores the data and runs your jobs.</p>
        </div>
        <button
          className="quiet-button"
          disabled={busy}
          onClick={() => void refresh()}
        >
          <ArrowsClockwise size={16} aria-hidden="true" />
          Refresh
        </button>
      </header>
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {!status && !error && <p role="status">Loading connection...</p>}
      {status && (
        <section className="hosted-panel" aria-label="Connection status">
          <h2>
            {status.state === "connected"
              ? "Connected"
              : status.state === "offline"
                ? "Server offline"
                : "No server connected"}
          </h2>
          <p>
            {status.remoteAccess
              ? "Remote access is enabled by your server."
              : "Remote access is disabled. Members open your server's own address."}
          </p>
          {status.instanceUrl && (
            <p className="hosted-connection-address">{status.instanceUrl}</p>
          )}
          <p>
            Webhooks use the outbound connection even when remote access is
            disabled.
          </p>
          {membership.role === "owner" && (
            <div className="hosted-row-actions">
              {status.state === "not_connected" && (
                <button
                  className="primary-button"
                  disabled={busy}
                  onClick={() => void createEnrollment()}
                >
                  {busy
                    ? "Creating token..."
                    : enrollment
                      ? "Generate another token"
                      : "Create connection token"}
                </button>
              )}
              {status.state !== "not_connected" && (
                <button
                  className="quiet-button"
                  disabled={busy}
                  onClick={() => setConfirmDisconnect(true)}
                >
                  Disconnect server
                </button>
              )}
            </div>
          )}
        </section>
      )}
      {enrollment && (
        <section className="hosted-panel hosted-connection-instructions">
          <h2>Connect your self-hosted installation</h2>
          <ol>
            <li>Sign in to your self-hosted installation as an owner.</li>
            <li>Open Settings, then Doodship connection.</li>
            <li>
              Enter the control plane address and connection token below, then
              connect.
            </li>
          </ol>
          <label>
            Control plane address
            <input readOnly value={enrollment.platformURL} />
          </label>
          <label>
            Connection token
            <input
              readOnly
              type="password"
              autoComplete="off"
              value={enrollment.enrollmentToken}
            />
          </label>
          <button
            className="quiet-button"
            onClick={() =>
              void navigator.clipboard
                .writeText(enrollment.enrollmentToken)
                .catch(() =>
                  setError(
                    "Copy failed. Select the token field and copy it manually.",
                  ),
                )
            }
          >
            Copy token
          </button>
          <p>
            Expires {new Date(enrollment.expiresAt).toLocaleTimeString()}. The
            token works once. It is only shown here.
          </p>
          <label>
            Your Doodship account ID
            <input readOnly value={enrollment.subject} />
          </label>
          <p>
            To allow remote access, map this account ID to a local user in your
            installation. Other members need their own mapping.
          </p>
        </section>
      )}
      {confirmDisconnect && (
        <section className="hosted-panel" aria-label="Confirm disconnection">
          <h2>Disconnect this server?</h2>
          <p>
            This stops access through Doodship and webhook forwarding. Local
            accounts and running jobs stay available.
          </p>
          <div className="hosted-row-actions">
            <button
              className="primary-button"
              disabled={busy}
              onClick={() => void disconnect()}
            >
              Disconnect
            </button>
            <button
              className="quiet-button"
              disabled={busy}
              onClick={() => setConfirmDisconnect(false)}
            >
              Cancel
            </button>
          </div>
        </section>
      )}
    </section>
  );
}
