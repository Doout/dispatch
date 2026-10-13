import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, ArrowsClockwise } from "@phosphor-icons/react";
import {
  hostedRequest,
  type InstanceConnectionStatus,
  type Membership,
} from "./client";
import { useSetupRefresh } from "./useSetupRefresh";

import { InstanceInstall, type Enrollment } from "./InstanceInstall";

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
          {status.state === "not_connected" ? (
            <p>
              Set up a new server with Docker, or connect an existing
              installation.
            </p>
          ) : (
            <>
              <p>
                {status.remoteAccess
                  ? "Remote access is enabled by your server."
                  : "Remote access is disabled. Members open your server's own address."}
              </p>
              {status.instanceUrl && (
                <p className="hosted-connection-address">
                  {status.instanceUrl}
                </p>
              )}
              <p>
                Webhooks use the outbound connection even when remote access is
                disabled.
              </p>
            </>
          )}
          {membership.role === "owner" && (
            <div className="hosted-row-actions">
              {status.state === "not_connected" && (
                <button
                  className="primary-button"
                  disabled={busy}
                  onClick={() => void createEnrollment()}
                >
                  {busy
                    ? "Preparing setup..."
                    : enrollment
                      ? "Generate another token"
                      : "Set up server"}
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
      {enrollment && status?.state === "not_connected" && (
        <InstanceInstall
          key={enrollment.enrollmentToken}
          enrollment={enrollment}
          instanceID={membership.tenant.id}
          onError={setError}
        />
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
