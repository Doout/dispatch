import { useEffect, useState } from "react";
import {
  CheckCircle,
  FolderSimple,
  GithubLogo,
  Key,
  LinkSimple,
  PencilSimple,
  ShieldCheck,
  UserSwitch,
  UsersThree,
  X,
} from "@phosphor-icons/react";
import { AccountAuthLink, AccountProfile, api } from "./api";
import { useDialogFocus } from "./useDialogFocus";

function providerHost(value: string) {
  try {
    return new URL(value).host;
  } catch {
    return value;
  }
}

function profileInitials(value: string) {
  const parts = value.trim().split(/[\s_-]+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return `${parts[0][0]}${parts[parts.length - 1][0]}`.toUpperCase();
}

function linkReturnTo() {
  const url = new URL(window.location.href);
  url.searchParams.delete("accountLink");
  url.searchParams.delete("detail");
  return `${url.pathname}${url.search}${url.hash}`;
}

function roleLabel(value: string) {
  return value
    .split("-")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

export function AccountProfileDialog({
  userID,
  readOnly = false,
  notice = "",
  initialError = "",
  onEdit,
  onImpersonate,
  onChangePassword,
  onClose,
}: {
  userID?: string;
  readOnly?: boolean;
  notice?: string;
  initialError?: string;
  onEdit?: () => void;
  onImpersonate?: () => void;
  onChangePassword?: () => void;
  onClose: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const [profile, setProfile] = useState<AccountProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [busyID, setBusyID] = useState("");
  const [error, setError] = useState(initialError);
  const isSelf = !userID;

  useEffect(() => {
    const request = userID ? api.userProfile(userID) : api.accountProfile();
    void request
      .then(setProfile)
      .catch((cause) => setError((cause as Error).message))
      .finally(() => setLoading(false));
  }, [userID]);

  async function link(item: AccountAuthLink) {
    setBusyID(item.provider.id);
    setError("");
    try {
      const result = await api.startOAuthLink(
        item.provider.id,
        linkReturnTo(),
      );
      window.location.assign(result.authorizationUrl);
    } catch (cause) {
      setError((cause as Error).message);
      setBusyID("");
    }
  }

  async function unlink(item: AccountAuthLink) {
    setBusyID(item.provider.id);
    setError("");
    try {
      await api.unlinkAuthProvider(item.provider.id);
      setProfile((current) =>
        current
          ? {
              ...current,
              links: current.links.map((candidate) =>
                candidate.provider.id === item.provider.id
                  ? { ...candidate, identity: undefined }
                  : candidate,
              ),
            }
          : current,
      );
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusyID("");
    }
  }

  const linked = profile?.links.filter((item) => item.identity) ?? [];
  const available =
    profile?.links.filter((item) => item.available && !item.identity) ?? [];
  const name = profile?.user.displayName || profile?.user.username || "Profile";

  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog account-profile-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-profile-title"
      >
        <header>
          <div className="account-profile-heading">
            <span className="account-profile-avatar" aria-hidden="true">
              {profileInitials(name)}
            </span>
            <div>
              <h2 id="account-profile-title">{name}</h2>
              {profile && (
                <p>
                  {profile.user.username}
                  {profile.user.email ? ` - ${profile.user.email}` : ""}
                </p>
              )}
            </div>
          </div>
          <button aria-label="Close profile" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body account-profile-body">
          {notice && (
            <p className="form-success account-profile-notice" role="status">
              <CheckCircle size={16} weight="fill" />
              {notice}
            </p>
          )}
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          {loading ? (
            <div className="account-profile-empty">Loading profile...</div>
          ) : profile ? (
            <>
              <dl className="account-profile-summary">
                <div>
                  <dt>Controller role</dt>
                  <dd>{roleLabel(profile.user.systemRole)}</dd>
                </div>
                <div>
                  <dt>Status</dt>
                  <dd>{roleLabel(profile.user.state)}</dd>
                </div>
              </dl>

              <section className="account-profile-section">
                <header>
                  <div>
                    <ShieldCheck size={18} />
                    <h3>Sign-in methods</h3>
                  </div>
                  <span>{linked.length + (profile.user.passwordConfigured ? 1 : 0)}</span>
                </header>
                <div className="account-profile-list">
                  {profile.user.passwordConfigured && (
                    <article>
                      <span className="account-profile-method-icon">
                        <Key size={18} />
                      </span>
                      <div>
                        <strong>{profile.managed ? "Controller credentials" : "Password"}</strong>
                        <small>{profile.managed ? "Managed on this controller" : "Local account"}</small>
                      </div>
                      <span className="access-state active">Active</span>
                    </article>
                  )}
                  {linked.map((item) => (
                    <article key={item.provider.id}>
                      <span className="account-profile-method-icon">
                        <GithubLogo size={18} weight="fill" />
                      </span>
                      <div>
                        <strong>{item.provider.name}</strong>
                        <small>
                          {item.identity?.email || item.identity?.login}
                        </small>
                      </div>
                      {isSelf && !readOnly ? (
                        <button
                          type="button"
                          className="quiet-button"
                          disabled={busyID === item.provider.id}
                          onClick={() => void unlink(item)}
                        >
                          {busyID === item.provider.id ? "Removing..." : "Remove"}
                        </button>
                      ) : (
                        <span className="access-state active">Linked</span>
                      )}
                    </article>
                  ))}
                  {!profile.user.passwordConfigured && linked.length === 0 && (
                    <div className="account-profile-empty">No sign-in methods</div>
                  )}
                </div>
                {isSelf && !readOnly && available.length > 0 && (
                  <div className="account-profile-available">
                    {available.map((item) => (
                      <button
                        key={item.provider.id}
                        type="button"
                        className="quiet-button"
                        aria-label={`Link ${item.provider.name}`}
                        disabled={busyID === item.provider.id}
                        onClick={() => void link(item)}
                      >
                        <LinkSimple size={15} />
                        {busyID === item.provider.id
                          ? "Opening..."
                          : `Link ${item.provider.name}`}
                        <small>{providerHost(item.provider.baseUrl)}</small>
                      </button>
                    ))}
                  </div>
                )}
              </section>

              <section className="account-profile-section">
                <header>
                  <div>
                    <UsersThree size={18} />
                    <h3>Teams</h3>
                  </div>
                  <span>{profile.teams.length}</span>
                </header>
                {profile.teams.length ? (
                  <div className="account-profile-tags">
                    {profile.teams.map((team) => (
                      <span key={team.id}>
                        <strong>{team.name}</strong>
                        <small>{roleLabel(team.role)}</small>
                      </span>
                    ))}
                  </div>
                ) : (
                  <div className="account-profile-empty">No teams</div>
                )}
              </section>

              <section className="account-profile-section">
                <header>
                  <div>
                    <FolderSimple size={18} />
                    <h3>Project access</h3>
                  </div>
                  <span>
                    {profile.user.systemRole === "owner"
                      ? "All"
                      : profile.projectAccess.length}
                  </span>
                </header>
                {profile.user.systemRole === "owner" ? (
                  <div className="account-profile-access-row">
                    <strong>All projects</strong>
                    <small>Controller owner</small>
                  </div>
                ) : profile.projectAccess.length ? (
                  <div className="account-profile-access-list">
                    {profile.projectAccess.map((grant, index) => (
                      <div
                        className="account-profile-access-row"
                        key={`${grant.projectId}-${grant.source}-${index}`}
                      >
                        <strong>{grant.projectName || "Unknown project"}</strong>
                        <small>
                          {roleLabel(grant.role)}
                          {grant.source === "team" && grant.sourceName
                            ? ` through ${grant.sourceName}`
                            : ""}
                        </small>
                      </div>
                    ))}
                  </div>
                ) : (
                  <div className="account-profile-empty">No project access</div>
                )}
              </section>
            </>
          ) : null}
          <div className="dialog-actions account-profile-actions">
            {profile && !isSelf && onImpersonate && (
              <button
                type="button"
                className="quiet-button"
                onClick={onImpersonate}
              >
                <UserSwitch size={15} />
                View as user
              </button>
            )}
            {profile &&
              isSelf &&
              !readOnly &&
              !profile.managed &&
              onChangePassword && (
              <button
                type="button"
                className="quiet-button"
                onClick={onChangePassword}
              >
                <Key size={15} />
                Change password
              </button>
            )}
            {profile && !isSelf && onEdit && (
              <button type="button" className="quiet-button" onClick={onEdit}>
                <PencilSimple size={15} />
                Edit user
              </button>
            )}
            <button type="button" className="primary-button" onClick={onClose}>
              Done
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}
