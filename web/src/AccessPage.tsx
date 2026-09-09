import { FormEvent, useCallback, useEffect, useMemo, useState } from "react";
import {
  ArrowsMerge,
  CaretDown,
  CheckCircle,
  Clock,
  EnvelopeSimple,
  GithubLogo,
  Key,
  PencilSimple,
  Plus,
  Trash,
  UserPlus,
  UserSwitch,
  UsersThree,
  X,
} from "@phosphor-icons/react";
import * as DropdownMenu from "@radix-ui/react-dropdown-menu";
import {
  AccessOverview,
  api,
  AuthProvider,
  RoleAssignment,
  Team,
  User,
} from "./api";
import { PageHeader } from "./PageHeader";
import { useDialogFocus } from "./useDialogFocus";
import { SignInMethods } from "./SignInMethods";
import { AccountProfileDialog } from "./AccountProfileDialog";

type Section = "users" | "teams" | "signin";
type Editor =
  { kind: "user"; item?: User } | { kind: "team"; item?: Team } | null;
type DeleteTarget =
  { kind: "user"; item: User } | { kind: "team"; item: Team } | null;
type ProjectRoles = Record<string, RoleAssignment["role"] | "">;

function userAuthTypes(access: AccessOverview, user: User): Set<string> {
  const types = new Set<string>();
  if (user.passwordConfigured) types.add("local");
  for (const identity of access.identities) {
    if (identity.userId !== user.id) continue;
    const provider = access.providers.find(
      (item) => item.id === identity.providerId,
    );
    if (provider) types.add(provider.type);
  }
  return types;
}

function canMergeUsers(
  access: AccessOverview,
  source: User,
  target: User,
): boolean {
  if (source.id === target.id || target.state !== "active") return false;
  if (source.systemRole === "owner" && target.systemRole !== "owner") {
    return false;
  }
  const sourceTypes = userAuthTypes(access, source);
  const targetTypes = userAuthTypes(access, target);
  return [...sourceTypes].every((type) => !targetTypes.has(type));
}

function sectionFromURL(): Section {
  const value = new URLSearchParams(window.location.search).get(
    "accessSection",
  );
  return value === "teams" || value === "signin" ? value : "users";
}

function reviewingPendingFromURL(): boolean {
  return (
    new URLSearchParams(window.location.search).get("accessReview") ===
    "pending"
  );
}

export function AccessPage({
  identityID,
  onChangePassword,
  onImpersonate,
}: {
  identityID?: string;
  onChangePassword?: () => void;
  onImpersonate?: (user: User) => void;
}) {
  const [access, setAccess] = useState<AccessOverview | null>(null);
  const [section, setSection] = useState<Section>(sectionFromURL);
  const [reviewingPending, setReviewingPending] = useState(
    reviewingPendingFromURL,
  );
  const [notice] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    const status = params.get("authProviderStatus");
    if (!status) return "";
    return status === "created"
      ? "Sign-in method created."
      : params.get("detail") || "GitHub setup failed.";
  });
  const [editor, setEditor] = useState<Editor>(null);
  const [deleteTarget, setDeleteTarget] = useState<DeleteTarget>(null);
  const [mergeSource, setMergeSource] = useState<User | null>(null);
  const [viewingUser, setViewingUser] = useState<User | null>(null);
  const [invitingExternal, setInvitingExternal] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [approving, setApproving] = useState("");

  const load = useCallback(async () => {
    try {
      setAccess(await api.access());
      setError("");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    const restoreSection = () => {
      setSection(sectionFromURL());
      setReviewingPending(reviewingPendingFromURL());
    };
    window.addEventListener("popstate", restoreSection);
    return () => window.removeEventListener("popstate", restoreSection);
  }, []);

  const selectSection = (next: Section) => {
    if (next === section && !reviewingPending) return;
    const url = new URL(window.location.href);
    url.searchParams.set("accessSection", next);
    url.searchParams.delete("accessReview");
    window.history.pushState({}, "", `${url.pathname}${url.search}${url.hash}`);
    setSection(next);
    setReviewingPending(false);
  };
  const reviewPendingUsers = () => {
    const url = new URL(window.location.href);
    url.searchParams.set("accessSection", "users");
    url.searchParams.set("accessReview", "pending");
    window.history.pushState({}, "", `${url.pathname}${url.search}${url.hash}`);
    setSection("users");
    setReviewingPending(true);
  };
  const pending = access?.users.filter((user) => user.state === "pending") ?? [];
  const activeMembers =
    access?.users.filter((user) => user.state === "active").length ?? 0;

  const approve = async (user: User) => {
    setApproving(user.id);
    setError("");
    try {
      await api.updateUser(user.id, {
        username: user.username,
        displayName: user.displayName,
        email: user.email ?? "",
        systemRole: user.systemRole,
        state: "active",
      });
      await load();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setApproving("");
    }
  };

  return (
    <div className="page-layout access-page">
      <PageHeader
        view="access"
        trailing={
          section === "users" ? (
            <UserActionsMenu
              pendingCount={pending.length}
              onAddLocal={() => setEditor({ kind: "user" })}
              onInviteExternal={() => setInvitingExternal(true)}
              onReviewPending={reviewPendingUsers}
            />
          ) : section === "teams" ? (
            <button
              className="primary-button"
              onClick={() => setEditor({ kind: "team" })}
            >
              <UsersThree size={16} />
              Add team
            </button>
          ) : undefined
        }
      />
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p
          className={
            new URLSearchParams(window.location.search).get(
              "authProviderStatus",
            ) === "created"
              ? "form-success"
              : "form-error"
          }
          role="status"
        >
          {notice}
        </p>
      )}
      <dl className="resource-summary">
        <div>
          <dd>{activeMembers}</dd>
          <dt>active members</dt>
        </div>
        <div>
          <dd>{access?.teams.length ?? 0}</dd>
          <dt>teams</dt>
        </div>
        <div className={pending.length ? "attention" : undefined}>
          <dd>{pending.length}</dd>
          <dt>pending approval</dt>
        </div>
        <div>
          <dd>{access?.assignments.length ?? 0}</dd>
          <dt>project grants</dt>
        </div>
      </dl>
      <nav className="application-sections" aria-label="Access sections">
        <button
          className={section === "users" ? "active" : ""}
          onClick={() => selectSection("users")}
        >
          Users <span>{access?.users.length ?? 0}</span>
        </button>
        <button
          className={section === "teams" ? "active" : ""}
          onClick={() => selectSection("teams")}
        >
          Teams <span>{access?.teams.length ?? 0}</span>
        </button>
        <button
          className={section === "signin" ? "active" : ""}
          onClick={() => selectSection("signin")}
        >
          Sign-in methods <span>{access?.providers?.length ?? 0}</span>
        </button>
      </nav>
      <section className="access-surface" aria-busy={loading}>
        {loading && <div className="access-empty">Loading access...</div>}
        {!loading && access && section === "users" && (
          <UsersTable
            access={access}
            identityID={identityID}
            approving={approving}
            onApprove={(item) => void approve(item)}
            onView={setViewingUser}
            onImpersonate={onImpersonate}
            onEdit={(item) => setEditor({ kind: "user", item })}
            onMerge={setMergeSource}
            onDelete={(item) => setDeleteTarget({ kind: "user", item })}
            pendingOnly={reviewingPending}
            onShowAll={() => selectSection("users")}
          />
        )}
        {!loading && access && section === "teams" && (
          <TeamsTable
            access={access}
            onEdit={(item) => setEditor({ kind: "team", item })}
            onDelete={(item) => setDeleteTarget({ kind: "team", item })}
          />
        )}
        {!loading && access && section === "signin" && (
          <SignInMethods
            providers={access.providers ?? []}
            onChanged={() => void load()}
          />
        )}
      </section>
      {editor && access && (
        <AccessEditor
          access={access}
          editor={editor}
          identityID={identityID}
          onChangePassword={() => {
            setEditor(null);
            onChangePassword?.();
          }}
          onClose={() => setEditor(null)}
          onSaved={async () => {
            setEditor(null);
            await load();
          }}
        />
      )}
      {deleteTarget && (
        <AccessDeleteDialog
          target={deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onDeleted={async () => {
            setDeleteTarget(null);
            await load();
          }}
        />
      )}
      {mergeSource && access && (
        <UserMergeDialog
          access={access}
          source={mergeSource}
          preferredTargetID={identityID}
          onClose={() => setMergeSource(null)}
          onMerged={async () => {
            setMergeSource(null);
            await load();
          }}
        />
      )}
      {viewingUser && (
        <AccountProfileDialog
          userID={viewingUser.id}
          onImpersonate={
            onImpersonate &&
            viewingUser.id !== identityID &&
            viewingUser.state === "active"
              ? () => onImpersonate(viewingUser)
              : undefined
          }
          onEdit={() => {
            setViewingUser(null);
            setEditor({ kind: "user", item: viewingUser });
          }}
          onClose={() => setViewingUser(null)}
        />
      )}
      {invitingExternal && access && (
        <ExternalInviteDialog
          providers={access.providers}
          onClose={() => setInvitingExternal(false)}
          onConfigure={() => {
            setInvitingExternal(false);
            selectSection("signin");
          }}
        />
      )}
    </div>
  );
}

function UserActionsMenu({
  pendingCount,
  onAddLocal,
  onInviteExternal,
  onReviewPending,
}: {
  pendingCount: number;
  onAddLocal: () => void;
  onInviteExternal: () => void;
  onReviewPending: () => void;
}) {
  return (
    <DropdownMenu.Root>
      <DropdownMenu.Trigger asChild>
        <button className="primary-button access-user-actions" type="button">
          <UsersThree size={17} />
          Manage users
          <CaretDown className="access-user-actions-caret" size={13} />
        </button>
      </DropdownMenu.Trigger>
      <DropdownMenu.Portal>
        <DropdownMenu.Content
          className="action-menu-list access-user-actions-menu"
          align="end"
          sideOffset={6}
          collisionPadding={12}
        >
          <DropdownMenu.Item
            className="action-menu-item"
            onSelect={onAddLocal}
          >
            <UserPlus size={17} />
            <span>
              <strong>Add local user</strong>
              <small>Create a password account.</small>
            </span>
          </DropdownMenu.Item>
          <DropdownMenu.Item
            className="action-menu-item"
            onSelect={onInviteExternal}
          >
            <EnvelopeSimple size={17} />
            <span>
              <strong>Invite external user</strong>
              <small>Share a provider sign-in link.</small>
            </span>
          </DropdownMenu.Item>
          <DropdownMenu.Separator className="action-menu-separator" />
          <DropdownMenu.Item
            className="action-menu-item"
            disabled={pendingCount === 0}
            onSelect={onReviewPending}
          >
            <Clock size={17} />
            <span>
              <strong>Review pending users</strong>
              <small>
                {pendingCount
                  ? `${pendingCount} ${pendingCount === 1 ? "request" : "requests"}`
                  : "No pending requests"}
              </small>
            </span>
          </DropdownMenu.Item>
        </DropdownMenu.Content>
      </DropdownMenu.Portal>
    </DropdownMenu.Root>
  );
}

function externalSignInURL(providerID: string): string {
  const url = new URL("/", window.location.origin);
  url.searchParams.set("authProvider", providerID);
  return url.toString();
}

function ExternalInviteDialog({
  providers,
  onClose,
  onConfigure,
}: {
  providers: AuthProvider[];
  onClose: () => void;
  onConfigure: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const readyProviders = providers.filter((provider) => provider.state === "ready");
  const [providerID, setProviderID] = useState(readyProviders[0]?.id ?? "");
  const [copied, setCopied] = useState(false);
  const provider = readyProviders.find((item) => item.id === providerID);
  const signInURL = provider ? externalSignInURL(provider.id) : "";
  const copy = async () => {
    if (!signInURL) return;
    await navigator.clipboard.writeText(signInURL);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1600);
  };
  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog access-dialog access-invite-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="external-invite-title"
      >
        <header>
          <div>
            <h2 id="external-invite-title">Invite external user</h2>
          </div>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          {readyProviders.length ? (
            <div className="access-invite-content">
              <label className="access-invite-field">
                <span>Sign-in method</span>
                <DropdownMenu.Root>
                  <DropdownMenu.Trigger asChild>
                    <button className="signin-host-trigger" type="button">
                      <span>
                        <GithubLogo size={17} weight="fill" />
                        {provider?.name}
                      </span>
                      <CaretDown size={14} />
                    </button>
                  </DropdownMenu.Trigger>
                  <DropdownMenu.Portal>
                    <DropdownMenu.Content
                      className="action-menu-list signin-host-menu"
                      align="start"
                      sideOffset={5}
                      collisionPadding={12}
                    >
                      {readyProviders.map((item) => (
                        <DropdownMenu.Item
                          className="action-menu-item"
                          key={item.id}
                          onSelect={() => setProviderID(item.id)}
                        >
                          <GithubLogo size={16} weight="fill" />
                          <span>
                            <strong>{item.name}</strong>
                            <small>{new URL(item.baseUrl).host}</small>
                          </span>
                          {item.id === providerID && (
                            <CheckCircle
                              className="signin-host-check"
                              size={15}
                              weight="fill"
                            />
                          )}
                        </DropdownMenu.Item>
                      ))}
                    </DropdownMenu.Content>
                  </DropdownMenu.Portal>
                </DropdownMenu.Root>
              </label>
              <label className="access-invite-field">
                <span>Sign-in link</span>
                <input readOnly value={signInURL} onFocus={(event) => event.currentTarget.select()} />
              </label>
              <p className="access-invite-note">
                The first sign-in creates a pending request. An owner must approve it.
              </p>
              <div className="dialog-actions">
                <button className="quiet-button" type="button" onClick={onClose}>
                  Cancel
                </button>
                <button className="primary-button" type="button" onClick={() => void copy()}>
                  {copied ? "Copied" : "Copy invite link"}
                </button>
              </div>
            </div>
          ) : (
            <div className="access-invite-empty">
              <div>
                <strong>No external sign-in methods</strong>
                <span>Configure GitHub OAuth before creating an invite.</span>
              </div>
              <div className="dialog-actions">
                <button className="quiet-button" type="button" onClick={onClose}>
                  Cancel
                </button>
                <button className="primary-button" type="button" onClick={onConfigure}>
                  Configure sign-in method
                </button>
              </div>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}

function AccessDeleteDialog({
  target,
  onClose,
  onDeleted,
}: {
  target: NonNullable<DeleteTarget>;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const name =
    target.kind === "user" ? target.item.displayName : target.item.name;
  const pendingRequest =
    target.kind === "user" && target.item.state === "pending";
  const remove = async () => {
    setBusy(true);
    setError("");
    try {
      if (target.kind === "user") await api.deleteUser(target.item.id);
      else await api.deleteTeam(target.item.id);
      onDeleted();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };
  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog confirm-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="access-delete-title"
      >
        <header>
          <h2 id="access-delete-title">
            {pendingRequest ? "Remove request" : `Remove ${target.kind}`}
          </h2>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          <p>
            <strong>{name}</strong>{" "}
            {pendingRequest
              ? "will not be able to sign in."
              : target.kind === "team"
                ? "will be removed with its project grants."
                : "will be removed with their team memberships and project grants."}
          </p>
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div className="dialog-actions confirm-actions">
            <button className="quiet-button" onClick={onClose}>
              Cancel
            </button>
            <button
              className="danger-button"
              disabled={busy}
              onClick={() => void remove()}
            >
              {busy
                ? "Removing..."
                : pendingRequest
                  ? "Remove request"
                  : "Remove"}
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}

function UsersTable({
  access,
  identityID,
  approving,
  pendingOnly,
  onApprove,
  onView,
  onImpersonate,
  onEdit,
  onMerge,
  onDelete,
  onShowAll,
}: {
  access: AccessOverview;
  identityID?: string;
  approving: string;
  pendingOnly: boolean;
  onApprove: (user: User) => void;
  onView: (user: User) => void;
  onImpersonate?: (user: User) => void;
  onEdit: (user: User) => void;
  onMerge: (user: User) => void;
  onDelete: (user: User) => void;
  onShowAll: () => void;
}) {
  const teamNames = (userID: string) =>
    access.members
      .filter((member) => member.userId === userID)
      .map(
        (member) =>
          access.teams.find((team) => team.id === member.teamId)?.name,
      )
      .filter(Boolean) as string[];
  const grants = (userID: string) =>
    access.assignments.filter(
      (item) => item.principalType === "user" && item.principalId === userID,
    );
  const pending = access.users.filter((user) => user.state === "pending");
  const users = access.users.filter((user) => user.state !== "pending");
  if (access.users.length === 0)
    return (
      <div className="access-empty">
        <strong>No members</strong>
        <span>Add a member or configure an external sign-in method.</span>
      </div>
    );
  if (pendingOnly && pending.length === 0)
    return (
      <div className="access-empty">
        <strong>No pending users</strong>
        <button className="quiet-button" type="button" onClick={onShowAll}>
          Show all users
        </button>
      </div>
    );
  return (
    <div className="access-users-view">
      {pending.length > 0 && (
        <section className="access-approval-queue" aria-label="Pending approval">
          <header>
            <div>
              <Clock size={17} />
              <strong>Pending approval</strong>
              <span>{pending.length}</span>
            </div>
            {pendingOnly ? (
              <button
                className="access-approval-filter-button"
                type="button"
                onClick={onShowAll}
              >
                Show all users
              </button>
            ) : (
              <small>Review project access before approval.</small>
            )}
          </header>
          <div>
            {pending.map((user) => (
              <article key={user.id}>
                <div className="access-approval-identity">
                  <button type="button" onClick={() => onView(user)}>
                    <strong>{user.displayName}</strong>
                  </button>
                  <span>
                    {user.email || user.username}
                    {(() => {
                      const identity = access.identities.find(
                        (item) => item.userId === user.id,
                      );
                      const provider = access.providers.find(
                        (item) => item.id === identity?.providerId,
                      );
                      return provider ? ` via ${provider.name}` : "";
                    })()}
                  </span>
                </div>
                <div className="access-approval-actions">
                  <button
                    className="quiet-button"
                    onClick={() => onEdit(user)}
                  >
                    Review
                  </button>
                  <button
                    className="primary-button"
                    disabled={approving === user.id}
                    onClick={() => onApprove(user)}
                  >
                    <CheckCircle size={16} weight="fill" />
                    {approving === user.id ? "Approving..." : "Approve"}
                  </button>
                  <button
                    className="access-icon-button danger"
                    aria-label={`Remove ${user.displayName}`}
                    title="Remove request"
                    onClick={() => onDelete(user)}
                  >
                    <Trash size={16} />
                  </button>
                </div>
              </article>
            ))}
          </div>
        </section>
      )}
      {!pendingOnly && users.length === 0 ? (
        <div className="access-empty access-members-empty">
          <strong>No approved members</strong>
          <span>Approve a request or add a local member.</span>
        </div>
      ) : !pendingOnly ? (
        <div className="access-table-wrap">
          <table className="access-table">
            <thead>
              <tr>
                <th>User</th>
                <th>Controller role</th>
                <th>Teams</th>
                <th>Direct access</th>
                <th>Status</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {users.map((user) => {
                const teams = teamNames(user.id);
                const direct = grants(user.id);
                const hasMergeTarget = users.some((target) =>
                  canMergeUsers(access, user, target),
                );
                return (
                  <tr key={user.id}>
                    <td data-label="User">
                      <button
                        type="button"
                        className="access-user-link"
                        aria-label={`View ${user.displayName || user.username} profile`}
                        onClick={() => onView(user)}
                      >
                        <strong>{user.displayName}</strong>
                        <small>
                          {user.username}
                          {user.email ? ` - ${user.email}` : ""}
                        </small>
                      </button>
                    </td>
                    <td data-label="Controller role">
                      {user.systemRole === "owner" ? (
                        <span className="access-role owner">Owner</span>
                      ) : (
                        <span className="access-role">Member</span>
                      )}
                    </td>
                    <td data-label="Teams">
                      {teams.length ? (
                        teams.join(", ")
                      ) : (
                        <span className="muted-cell">None</span>
                      )}
                    </td>
                    <td data-label="Direct access">
                      {user.systemRole === "owner" ? (
                        <span className="access-role owner">All projects</span>
                      ) : (
                        <GrantList
                          access={access}
                          grants={direct}
                          empty={teams.length ? "Team grants only" : "None"}
                        />
                      )}
                    </td>
                    <td data-label="Status">
                      <span className={`access-state ${user.state}`}>
                        {user.state === "active" ? "Active" : "Disabled"}
                      </span>
                    </td>
                    <td data-label="Actions">
                      <div className="access-row-actions">
                        {user.id !== identityID &&
                          user.state === "active" &&
                          onImpersonate && (
                            <button
                              aria-label={`View as ${user.displayName}`}
                              title="View as user"
                              onClick={() => onImpersonate(user)}
                            >
                              <UserSwitch size={16} />
                            </button>
                          )}
                        <button
                          aria-label={`Edit ${user.displayName}`}
                          title="Edit"
                          onClick={() => onEdit(user)}
                        >
                          <PencilSimple size={16} />
                        </button>
                        {user.id !== identityID && hasMergeTarget && (
                          <button
                            aria-label={`Merge ${user.displayName}`}
                            title="Merge user"
                            onClick={() => onMerge(user)}
                          >
                            <ArrowsMerge size={16} />
                          </button>
                        )}
                        <button
                          className="danger"
                          aria-label={`Remove ${user.displayName}`}
                          title="Remove"
                          onClick={() => onDelete(user)}
                        >
                          <Trash size={16} />
                        </button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}

function UserMergeDialog({
  access,
  source,
  preferredTargetID,
  onClose,
  onMerged,
}: {
  access: AccessOverview;
  source: User;
  preferredTargetID?: string;
  onClose: () => void;
  onMerged: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const candidates = access.users.filter(
    (user) => canMergeUsers(access, source, user),
  );
  const preferred = candidates.find((user) => user.id === preferredTargetID);
  const owner = candidates.find((user) => user.systemRole === "owner");
  const [targetID, setTargetID] = useState(
    preferred?.id ?? owner?.id ?? candidates[0]?.id ?? "",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const target = candidates.find((user) => user.id === targetID);
  const sourceIdentities = access.identities.filter(
    (identity) => identity.userId === source.id,
  );
  const sourceTeams = access.members.filter(
    (member) => member.userId === source.id,
  ).length;
  const sourceGrants = access.assignments.filter(
    (assignment) =>
      assignment.principalType === "user" &&
      assignment.principalId === source.id,
  ).length;

  async function merge() {
    if (!targetID) return;
    setBusy(true);
    setError("");
    try {
      await api.mergeUser(source.id, targetID);
      onMerged();
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  }

  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog merge-user-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="merge-user-title"
      >
        <header>
          <div>
            <h2 id="merge-user-title">Merge user</h2>
            <p>Keep one user and move this account into it.</p>
          </div>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body merge-user-body">
          <div className="merge-user-path">
            <div>
              <small>Move</small>
              <strong>{source.displayName}</strong>
              <span>{source.email || source.username}</span>
            </div>
            <ArrowsMerge size={22} />
            <label>
              <span>Keep</span>
              <select
                aria-label="User to keep"
                value={targetID}
                onChange={(event) => setTargetID(event.target.value)}
              >
                {candidates.map((user) => (
                  <option key={user.id} value={user.id}>
                    {user.displayName} ({user.username})
                  </option>
                ))}
              </select>
            </label>
          </div>
          <dl className="merge-user-summary">
            <div>
              <dt>Sign-in methods</dt>
              <dd>{sourceIdentities.length}</dd>
            </div>
            <div>
              <dt>Teams</dt>
              <dd>{sourceTeams}</dd>
            </div>
            <div>
              <dt>Direct grants</dt>
              <dd>{sourceGrants}</dd>
            </div>
          </dl>
          {target && (
            <p className="merge-user-warning">
              <strong>{target.displayName}</strong> keeps its profile, role, and
              password. <strong>{source.displayName}</strong> is removed.
            </p>
          )}
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          <div className="dialog-actions">
            <button type="button" className="quiet-button" onClick={onClose}>
              Cancel
            </button>
            <button
              type="button"
              className="primary-button"
              disabled={!targetID || busy}
              onClick={() => void merge()}
            >
              <ArrowsMerge size={16} />
              {busy ? "Merging..." : "Merge users"}
            </button>
          </div>
        </div>
      </section>
    </div>
  );
}

function TeamsTable({
  access,
  onEdit,
  onDelete,
}: {
  access: AccessOverview;
  onEdit: (team: Team) => void;
  onDelete: (team: Team) => void;
}) {
  if (access.teams.length === 0)
    return (
      <div className="access-empty">
        <strong>No teams</strong>
        <span>
          Create a team to grant the same project role to several users.
        </span>
      </div>
    );
  return (
    <div className="access-table-wrap">
      <table className="access-table">
        <thead>
          <tr>
            <th>Team</th>
            <th>Members</th>
            <th>Project access</th>
            <th>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {access.teams.map((team) => {
            const memberCount = access.members.filter(
              (member) => member.teamId === team.id,
            ).length;
            const grants = access.assignments.filter(
              (item) =>
                item.principalType === "team" && item.principalId === team.id,
            );
            return (
              <tr key={team.id}>
                <td data-label="Team">
                  <strong>{team.name}</strong>
                  {team.description && <small>{team.description}</small>}
                </td>
                <td data-label="Members">{memberCount}</td>
                <td data-label="Project access">
                  <GrantList access={access} grants={grants} />
                </td>
                <td data-label="Actions">
                  <div className="access-row-actions">
                    <button
                      aria-label={`Edit ${team.name}`}
                      title="Edit"
                      onClick={() => onEdit(team)}
                    >
                      <PencilSimple size={16} />
                    </button>
                    <button
                      className="danger"
                      aria-label={`Remove ${team.name}`}
                      title="Remove"
                      onClick={() => onDelete(team)}
                    >
                      <Trash size={16} />
                    </button>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function GrantList({
  access,
  grants,
  empty = "None",
}: {
  access: AccessOverview;
  grants: RoleAssignment[];
  empty?: string;
}) {
  if (!grants.length) return <span className="muted-cell">{empty}</span>;
  return (
    <div className="access-grants">
      {grants.map((grant) => {
        const project = access.projects.find(
          (item) => item.id === grant.scopeId,
        );
        const role = access.roles.find((item) => item.id === grant.role);
        return (
          <span
            key={grant.id}
            title={`${project?.name ?? "Unknown project"} - ${role?.name ?? grant.role}`}
          >
            <strong>{project?.name ?? "Unknown project"}</strong>
            <small>{role?.name ?? grant.role}</small>
          </span>
        );
      })}
    </div>
  );
}

function AccessEditor({
  access,
  editor,
  identityID,
  onChangePassword,
  onClose,
  onSaved,
}: {
  access: AccessOverview;
  editor: NonNullable<Editor>;
  identityID?: string;
  onChangePassword: () => void;
  onClose: () => void;
  onSaved: () => void;
}) {
  const dialogRef = useDialogFocus(onClose);
  const principalID = editor.item?.id ?? "";
  const existingAssignments = useMemo(
    () =>
      access.assignments.filter(
        (item) =>
          item.principalType === editor.kind &&
          item.principalId === principalID,
      ),
    [access.assignments, editor.kind, principalID],
  );
  const initialRoles = useMemo(
    () =>
      Object.fromEntries(
        existingAssignments.map((item) => [item.scopeId, item.role]),
      ) as ProjectRoles,
    [existingAssignments],
  );
  const [roles, setRoles] = useState<ProjectRoles>(initialRoles);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  return (
    <div
      className="dialog-layer access-dialog-layer"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className="resource-dialog access-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="access-dialog-title"
      >
        <header>
          <div>
            <h2 id="access-dialog-title">
              {editor.kind === "user" && editor.item?.state === "pending"
                ? "Review user"
                : editor.kind === "user" && !editor.item
                  ? "Add local user"
                : `${editor.item ? "Edit" : "Add"} ${editor.kind}`}
            </h2>
          </div>
          <button aria-label="Close dialog" onClick={onClose}>
            <X size={19} weight="bold" />
          </button>
        </header>
        <div className="dialog-body">
          {editor.kind === "user" ? (
            <UserEditor
              access={access}
              user={editor.item}
              currentUser={editor.item?.id === identityID}
              onChangePassword={onChangePassword}
              roles={roles}
              onRoles={setRoles}
              busy={busy}
              error={error}
              onCancel={onClose}
              onSubmit={async (input) => {
                setBusy(true);
                setError("");
                try {
                  const saved = editor.item
                    ? await api.updateUser(editor.item.id, input)
                    : await api.createUser({
                        ...input,
                        password: input.password ?? "",
                      });
                  await syncAssignments(
                    access,
                    "user",
                    saved.id,
                    roles,
                    existingAssignments,
                  );
                  onSaved();
                } catch (cause) {
                  setError((cause as Error).message);
                  setBusy(false);
                }
              }}
            />
          ) : (
            <TeamEditor
              access={access}
              team={editor.item}
              roles={roles}
              onRoles={setRoles}
              busy={busy}
              error={error}
              onCancel={onClose}
              onSubmit={async (input) => {
                setBusy(true);
                setError("");
                try {
                  const saved = editor.item
                    ? await api.updateTeam(editor.item.id, input)
                    : await api.createTeam(input);
                  await syncAssignments(
                    access,
                    "team",
                    saved.id,
                    roles,
                    existingAssignments,
                  );
                  onSaved();
                } catch (cause) {
                  setError((cause as Error).message);
                  setBusy(false);
                }
              }}
            />
          )}
        </div>
      </section>
    </div>
  );
}

function UserEditor({
  access,
  user,
  currentUser,
  onChangePassword,
  roles,
  onRoles,
  busy,
  error,
  onCancel,
  onSubmit,
}: {
  access: AccessOverview;
  user?: User;
  currentUser: boolean;
  onChangePassword: () => void;
  roles: ProjectRoles;
  onRoles: (roles: ProjectRoles) => void;
  busy: boolean;
  error: string;
  onCancel: () => void;
  onSubmit: (input: {
    username: string;
    displayName: string;
    email: string;
    password?: string;
    systemRole: User["systemRole"];
    state: User["state"];
  }) => void;
}) {
  const [username, setUsername] = useState(user?.username ?? "");
  const [displayName, setDisplayName] = useState(user?.displayName ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [password, setPassword] = useState("");
  const [systemRole, setSystemRole] = useState<User["systemRole"]>(
    user?.systemRole ?? "member",
  );
  const [state, setState] = useState<User["state"]>(user?.state ?? "active");
  const externalIdentity = access.identities.find(
    (item) => item.userId === user?.id,
  );
  const externalProvider = access.providers.find(
    (item) => item.id === externalIdentity?.providerId,
  );
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit({
      username,
      displayName,
      email,
      ...(!user ? { password } : {}),
      systemRole,
      state,
    });
  };
  return (
    <form className="resource-form access-form" onSubmit={submit}>
      {user?.state === "pending" && externalIdentity && (
        <div className="access-review-origin wide">
          <GithubLogo size={18} />
          <span>
            <strong>{externalProvider?.name ?? "External sign-in"}</strong>
            <small>{externalIdentity.email || externalIdentity.login}</small>
          </span>
        </div>
      )}
      <label>
        <span>Display name</span>
        <input
          value={displayName}
          onChange={(event) => setDisplayName(event.target.value)}
          required
          autoFocus
        />
      </label>
      <label>
        <span>Username</span>
        <input
          value={username}
          onChange={(event) => setUsername(event.target.value)}
          required
          autoComplete="off"
        />
      </label>
      <label className={user ? "wide" : undefined}>
        <span>Email</span>
        <input
          type="email"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
        />
      </label>
      {!user && (
        <label>
          <span>Password</span>
          <input
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            required
            minLength={12}
            autoComplete="new-password"
          />
        </label>
      )}
      <label>
        <span>Controller role</span>
        <select
          value={systemRole}
          onChange={(event) =>
            setSystemRole(event.target.value as User["systemRole"])
          }
        >
          <option value="member">Member</option>
          <option value="owner">Owner</option>
        </select>
      </label>
      <label>
        <span>Status</span>
        <select
          value={state}
          onChange={(event) => setState(event.target.value as User["state"])}
        >
          {user?.state === "pending" && (
            <option value="pending">Pending approval</option>
          )}
          <option value="active">Active</option>
          <option value="disabled">Disabled</option>
        </select>
      </label>
      {systemRole === "member" && (
        <ProjectAccess access={access} roles={roles} onChange={onRoles} />
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions access-form-actions">
        {user && currentUser && user.passwordConfigured && (
          <button
            type="button"
            className="access-password-button"
            onClick={onChangePassword}
          >
            <Key size={16} />
            Change password
          </button>
        )}
        <button type="button" className="quiet-button" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary-button" disabled={busy}>
          {busy
            ? user?.state === "pending" && state === "active"
              ? "Approving..."
              : "Saving..."
            : user?.state === "pending" && state === "active"
              ? "Approve user"
              : "Save user"}
        </button>
      </div>
    </form>
  );
}

function TeamEditor({
  access,
  team,
  roles,
  onRoles,
  busy,
  error,
  onCancel,
  onSubmit,
}: {
  access: AccessOverview;
  team?: Team;
  roles: ProjectRoles;
  onRoles: (roles: ProjectRoles) => void;
  busy: boolean;
  error: string;
  onCancel: () => void;
  onSubmit: (input: {
    name: string;
    description: string;
    memberIds: string[];
  }) => void;
}) {
  const [name, setName] = useState(team?.name ?? "");
  const [description, setDescription] = useState(team?.description ?? "");
  const [members, setMembers] = useState<string[]>(
    team
      ? access.members
          .filter((member) => member.teamId === team.id)
          .map((member) => member.userId)
      : [],
  );
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onSubmit({ name, description, memberIds: members });
  };
  return (
    <form className="resource-form access-form" onSubmit={submit}>
      <label>
        <span>Name</span>
        <input
          value={name}
          onChange={(event) => setName(event.target.value)}
          required
          autoFocus
        />
      </label>
      <label>
        <span>Description</span>
        <input
          value={description}
          onChange={(event) => setDescription(event.target.value)}
        />
      </label>
      <fieldset className="access-member-picker wide">
        <legend>Members</legend>
        <div>
          {access.users
            .filter((user) => user.state === "active")
            .map((user) => (
              <label key={user.id}>
                <input
                  type="checkbox"
                  checked={members.includes(user.id)}
                  onChange={(event) =>
                    setMembers((current) =>
                      event.target.checked
                        ? [...current, user.id]
                        : current.filter((id) => id !== user.id),
                    )
                  }
                />
                <span>
                  <strong>{user.displayName}</strong>
                  <small>{user.username}</small>
                </span>
              </label>
            ))}
        </div>
      </fieldset>
      <ProjectAccess access={access} roles={roles} onChange={onRoles} />
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-actions">
        <button type="button" className="quiet-button" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary-button" disabled={busy}>
          {busy ? "Saving..." : "Save team"}
        </button>
      </div>
    </form>
  );
}

function ProjectAccess({
  access,
  roles,
  onChange,
}: {
  access: AccessOverview;
  roles: ProjectRoles;
  onChange: (roles: ProjectRoles) => void;
}) {
  return (
    <fieldset className="access-projects wide">
      <legend>Project access</legend>
      {access.projects.length === 0 ? (
        <p>No projects.</p>
      ) : (
        <div>
          {access.projects.map((project) => (
            <label key={project.id}>
              <span>
                <strong>{project.name}</strong>
                {project.description && <small>{project.description}</small>}
              </span>
              <select
                aria-label={`${project.name} role`}
                value={roles[project.id] ?? ""}
                onChange={(event) =>
                  onChange({
                    ...roles,
                    [project.id]: event.target.value as
                      RoleAssignment["role"] | "",
                  })
                }
              >
                <option value="">No access</option>
                {access.roles.map((role) => (
                  <option value={role.id} key={role.id}>
                    {role.name}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </div>
      )}
    </fieldset>
  );
}

async function syncAssignments(
  access: AccessOverview,
  principalType: RoleAssignment["principalType"],
  principalId: string,
  roles: ProjectRoles,
  previous: RoleAssignment[],
) {
  const removals = previous.filter((item) => !roles[item.scopeId]);
  const writes = access.projects.filter(
    (project) =>
      roles[project.id] &&
      previous.find((item) => item.scopeId === project.id)?.role !==
        roles[project.id],
  );
  await Promise.all(removals.map((item) => api.deleteRoleAssignment(item.id)));
  await Promise.all(
    writes.map((project) =>
      api.upsertRoleAssignment({
        principalType,
        principalId,
        projectId: project.id,
        role: roles[project.id] as RoleAssignment["role"],
      }),
    ),
  );
}
