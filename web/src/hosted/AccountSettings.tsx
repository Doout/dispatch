import { useState, type FormEvent } from "react";
import { hostedRequest, type Account, type HostedPlatform } from "./client";
import { accountInitials } from "./HostedChrome";

export function AccountSettings({
  account,
  configuration,
  onPasswordChanged,
}: {
  account: Account;
  configuration?: HostedPlatform;
  onPasswordChanged: () => void;
}) {
  const [oldPassword, setOldPassword] = useState(""),
    [newPassword, setNewPassword] = useState(""),
    [confirmation, setConfirmation] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (newPassword !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await hostedRequest("/account/password", "PUT", {
        oldPassword,
        newPassword,
      });
      onPasswordChanged();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <header className="hosted-page-heading">
        <div>
          <h1>Account</h1>
          <p>Your profile and sign-in settings.</p>
        </div>
      </header>
      <div className="hosted-account-layout">
        <section className="hosted-panel hosted-profile">
          <span
            className="hosted-avatar hosted-avatar-large"
            aria-hidden="true"
          >
            {accountInitials(account.name)}
          </span>
          <h2>{account.name}</h2>
          <p>{account.email}</p>
          <dl>
            <div>
              <dt>Account ID</dt>
              <dd className="hosted-connection-address">{account.id}</dd>
            </div>
            <div>
              <dt>Platform role</dt>
              <dd>{account.platformAdmin ? "Platform admin" : "Member"}</dd>
            </div>
            <div>
              <dt>Email</dt>
              <dd>{account.emailVerified ? "Verified" : "Not verified"}</dd>
            </div>
          </dl>
        </section>
        {configuration?.authkitEnabled && (
          <section className="hosted-panel">
            <h2>Sign-in account</h2>
            <p>
              {account.workosLinked
                ? "Your account is connected to WorkOS."
                : "Connect your existing account to WorkOS to keep your memberships and access."}
            </p>
            {!account.workosLinked && (
              <a className="primary-button" href="/?view=login&link=workos">
                Connect WorkOS
              </a>
            )}
            {error && configuration.passwordLoginEnabled === false && (
              <p role="alert" className="form-error">
                {error}
              </p>
            )}
          </section>
        )}
        {configuration?.passwordLoginEnabled !== false && (
          <section className="hosted-panel hosted-password-panel">
            <h2>Change password</h2>
            <p className="hosted-panel-description">
              Use at least 12 characters. You'll sign in again after changing
              your password.
            </p>
            <form
              className="hosted-form"
              onSubmit={(event) => void submit(event)}
            >
              <label>
                Current password
                <input
                  type="password"
                  autoComplete="current-password"
                  value={oldPassword}
                  onChange={(event) => setOldPassword(event.target.value)}
                  required
                />
              </label>
              <label>
                New password
                <input
                  type="password"
                  autoComplete="new-password"
                  value={newPassword}
                  onChange={(event) => setNewPassword(event.target.value)}
                  required
                  minLength={12}
                />
              </label>
              <label>
                Confirm new password
                <input
                  type="password"
                  autoComplete="new-password"
                  value={confirmation}
                  onChange={(event) => setConfirmation(event.target.value)}
                  required
                  minLength={12}
                />
              </label>
              {error && (
                <p role="alert" className="form-error">
                  {error}
                </p>
              )}
              <button className="primary-button" disabled={busy}>
                {busy ? "Saving..." : "Change password"}
              </button>
            </form>
          </section>
        )}
      </div>
    </>
  );
}
