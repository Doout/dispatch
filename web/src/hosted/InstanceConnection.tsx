import { useCallback, useEffect, useState } from "react";
import { ArrowLeft, ArrowsClockwise } from "@phosphor-icons/react";
import {
  hostedRequest,
  type InstanceConnectionStatus,
  type Membership,
} from "./client";
import { useSetupRefresh } from "./useSetupRefresh";
import { InstanceActionDialog, type InstanceAction } from "./InstanceActions";

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
  const [action, setAction] = useState<InstanceAction | null>(null);
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
                  ? "Members can open this server through Doodship."
                  : "Members open this server at its own address."}
              </p>
              {status.instanceUrl && (
                <p className="hosted-connection-address">
                  {status.instanceUrl}
                </p>
              )}
              <p>Doodship forwards webhooks over this connection.</p>
            </>
          )}
          {membership.role === "owner" &&
            status.backendMode === "connected" && (
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
                    onClick={() => setAction("disconnect")}
                  >
                    Disconnect server
                  </button>
                )}
                <button
                  className="danger-button"
                  disabled={busy}
                  onClick={() => setAction("delete")}
                >
                  Delete instance
                </button>
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
      {action && (
        <InstanceActionDialog
          membership={{ ...membership, connection: status }}
          action={action}
          onClose={() => setAction(null)}
          onCompleted={async () => {
            setEnrollment(undefined);
            if (action === "delete") onClose();
            else await refresh();
            await onChanged();
          }}
        />
      )}
    </section>
  );
}
