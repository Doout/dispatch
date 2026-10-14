import { useState } from "react";
import { Copy, Check } from "@phosphor-icons/react";

export type Enrollment = {
  enrollmentToken: string;
  expiresAt: string;
  platformURL: string;
  subject: string;
};
export function installCommand(
  enrollment: Enrollment,
  instanceID: string,
  remoteAccess: boolean,
  directURL: string,
) {
  const connection = JSON.stringify({
    controlPlaneURL: enrollment.platformURL,
    enrollmentToken: enrollment.enrollmentToken,
    instanceID,
    ...(remoteAccess ? {} : { directURL: directURL.trim() }),
    remoteAccess,
  });
  const url = new URL("/install.sh", enrollment.platformURL).href;
  const quotedURL = "'" + url.replace(/'/g, "'\\''") + "'";
  // Fetch the whole script before executing it. Keep connection secrets on stdin.
  return `installer="$(curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 60 --retry 2 --retry-max-time 90 --max-filesize 65536 ${quotedURL})" && test -n "$installer" && sh -c "$installer" -- <<'DOODSHIP_CONNECTION'
${connection}
DOODSHIP_CONNECTION`;
}

export function InstanceInstall({
  enrollment,
  instanceID,
  onError,
}: {
  enrollment: Enrollment;
  instanceID: string;
  onError: (message: string) => void;
}) {
  const [method, setMethod] = useState("install");
  const [remoteAccess, setRemoteAccess] = useState(true);
  const [directURL, setDirectURL] = useState("");
  const [copied, setCopied] = useState("");
  const command = installCommand(
    enrollment,
    instanceID,
    remoteAccess,
    directURL,
  );
  const copy = async (value: string, label: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(label);
    } catch {
      onError("Copy failed. Select the command and copy it manually.");
    }
  };
  const expired = Date.now() >= new Date(enrollment.expiresAt).getTime();
  let validAddress = false;
  try {
    const url = new URL(directURL.trim());
    validAddress =
      ["http:", "https:"].includes(url.protocol) &&
      !!url.hostname &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash;
  } catch {
    /* The address is incomplete while typing. */
  }
  const ready = remoteAccess || validAddress;
  return (
    <section className="hosted-panel hosted-connection-instructions">
      <h2>Set up your server</h2>
      <p>
        Run the command on your Linux server. Docker must be installed and
        available to your user.
      </p>
      <div
        className="hosted-install-methods"
        role="tablist"
        aria-label="Setup method"
      >
        {[
          ["install", "New installation"],
          ["existing", "Existing installation"],
        ].map(([value, label]) => (
          <button
            key={value}
            role="tab"
            id={`install-${value}`}
            aria-controls="install-options"
            aria-selected={method === value}
            className="quiet-button"
            onClick={() => {
              setMethod(value);
              setCopied("");
            }}
          >
            {label}
          </button>
        ))}
      </div>
      <div
        id="install-options"
        role="tabpanel"
        aria-labelledby={`install-${method}`}
        className="hosted-install-options"
      >
        {method === "existing" ? (
          <>
            <p>
              Sign in to your installation as an owner. Open Settings →
              Connections → Doodship and enter these values.
            </p>
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
              onClick={() => void copy(enrollment.enrollmentToken, "token")}
            >
              {copied === "token" ? "Copied" : "Copy token"}
            </button>
            <label>
              Your Doodship account ID
              <input readOnly value={enrollment.subject} />
            </label>
            <p>
              Link this account to a local user to allow remote access. Other
              members need their own link.
            </p>
          </>
        ) : (
          <>
            <label className="hosted-install-access">
              <input
                type="checkbox"
                checked={remoteAccess}
                onChange={(event) => setRemoteAccess(event.target.checked)}
              />
              Allow members to open this server through Doodship
            </label>
            <p>
              Your server connects to Doodship. No public port is needed.
              Webhooks still work when remote access is off.
            </p>
            {!remoteAccess && (
              <label>
                Instance address
                <input
                  type="url"
                  placeholder="https://dispatch.example.com"
                  value={directURL}
                  onChange={(event) => setDirectURL(event.target.value)}
                />
                <small>Members open this address using their LAN or VPN.</small>
              </label>
            )}
            <pre className="hosted-install-command">
              <code>{command}</code>
            </pre>
            <button
              className="primary-button"
              disabled={expired || !ready}
              onClick={() => void copy(command, "command")}
            >
              {copied === "command" ? (
                <Check size={16} aria-hidden="true" />
              ) : (
                <Copy size={16} aria-hidden="true" />
              )}
              {copied === "command" ? "Copied" : "Copy install command"}
            </button>
            <p>
              Installs in <code>$HOME/.doodship</code> and enables daily Docker
              updates. Updates may interrupt running jobs. Your data stays on
              this server.
            </p>
            <p>
              Your account gets owner access. The recovery login is in
              <code> $HOME/.doodship/local-owner.json</code>.
            </p>
          </>
        )}
      </div>
      <p role={expired ? "alert" : undefined}>
        {expired
          ? "This token expired. Generate another token to install."
          : `Expires ${new Date(enrollment.expiresAt).toLocaleTimeString()}. One-use token. Keep this command private.`}
      </p>
    </section>
  );
}
