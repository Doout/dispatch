import { useState } from "react";
import { DownloadSimple, Copy, Check } from "@phosphor-icons/react";

export type Enrollment = {
  enrollmentToken: string;
  expiresAt: string;
  platformURL: string;
  subject: string;
};
const image = "ghcr.io/doout/dispatch:main";

export function installationFiles(
  enrollment: Enrollment,
  instanceID: string,
  remoteAccess: boolean,
  directURL: string,
) {
  const connection = JSON.stringify(
    {
      controlPlaneURL: enrollment.platformURL,
      enrollmentToken: enrollment.enrollmentToken,
      instanceID,
      directURL: directURL.trim(),
      remoteAccess,
    },
    null,
    2,
  );
  const docker = `mkdir -p "$HOME/.doodship" && chmod 700 "$HOME/.doodship" &&
docker run --rm -i --user 0 --network host \\
  --mount "type=bind,src=$HOME/.doodship,dst=$HOME/.doodship" \\
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \\
  ${image} setup --dir "$HOME/.doodship" --name doodship \\
  --image ${image} --host-uid "$(id -u)" --host-gid "$(id -g)" --connection-stdin <<'DOODSHIP_CONNECTION'
${connection}
DOODSHIP_CONNECTION`;
  const compose = `services:
  setup:
    image: ${image}
    user: "0"
    network_mode: host
    volumes:
      - type: bind
        source: \${HOME}/.doodship
        target: \${HOME}/.doodship
      - type: bind
        source: /var/run/docker.sock
        target: /var/run/docker.sock
    command:
      - setup
      - --dir
      - \${HOME}/.doodship
      - --name
      - doodship
      - --image
      - ${image}
      - --host-uid
      - "\${DOODSHIP_UID:-0}"
      - --host-gid
      - "\${DOODSHIP_GID:-0}"
      - --connection-stdin
`;
  const composeCommand = `mkdir -p "$HOME/.doodship" && chmod 700 "$HOME/.doodship" &&
DOODSHIP_UID="$(id -u)" DOODSHIP_GID="$(id -g)" docker compose -f doodship-setup.yaml run --rm -T setup <<'DOODSHIP_CONNECTION'
${connection}
DOODSHIP_CONNECTION`;
  // Keep connection secrets out of the downloaded Compose file.
  return { docker, compose, composeCommand };
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
  const [method, setMethod] = useState("docker");
  const [remoteAccess, setRemoteAccess] = useState(true);
  const [directURL, setDirectURL] = useState("");
  const [copied, setCopied] = useState("");
  const files = installationFiles(
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
  const download = () => {
    const url = URL.createObjectURL(
      new Blob([files.compose], { type: "application/yaml" }),
    );
    const link = document.createElement("a");
    link.href = url;
    link.download = "doodship-setup.yaml";
    link.click();
    URL.revokeObjectURL(url);
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
  const command = method === "compose" ? files.composeCommand : files.docker;
  return (
    <section className="hosted-panel hosted-connection-instructions">
      <h2>Set up your server</h2>
      <p>
        Run this on a Linux server with Docker. Setup installs the CLI and
        configuration in <code>$HOME/.doodship</code>, starts your server, and
        enables daily updates. Your database, encryption key, and job data stay
        in its Docker volume.
      </p>
      <div
        className="hosted-install-methods"
        role="tablist"
        aria-label="Setup method"
      >
        {[
          ["docker", "Docker command"],
          ["compose", "Docker Compose"],
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
              Connections start from your server. Webhook forwarding works with
              remote access turned off.
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
            {method === "compose" && (
              <button className="quiet-button" onClick={download}>
                <DownloadSimple size={16} aria-hidden="true" />
                Download Compose file
              </button>
            )}
            <p>
              {method === "compose"
                ? "Save doodship-setup.yaml on your server, then run:"
                : "Run in your server's terminal:"}
            </p>
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
              Setup links your Doodship account to the local owner. The recovery
              login is saved in <code>$HOME/.doodship/local-owner.json</code>.
            </p>
            <p>
              Requires Linux amd64 or arm64 and Docker access. No systemd
              service or public port is needed. Daily updates may briefly
              interrupt jobs.
            </p>
          </>
        )}
      </div>
      <p role={expired ? "alert" : undefined}>
        {expired
          ? "This token expired. Generate another token to install."
          : `Expires ${new Date(enrollment.expiresAt).toLocaleTimeString()}. This command contains a one-use token. Keep it private.`}
      </p>
    </section>
  );
}
