import { useEffect, useRef, useState, type FormEvent } from "react";
import { GithubLogo, GoogleLogo } from "@phosphor-icons/react";
import {
  hostedRequest,
  startManagedLogin,
  type HostedPlatform,
} from "./client";
import { PasswordField } from "./PasswordField";

type Step =
  | "login"
  | "email-code"
  | "verify-email"
  | "totp"
  | "organization"
  | "link-account"
  | "waitlist"
  | "forgot-password"
  | "reset-password";
type Progress = {
  step:
    | "email-code"
    | "verify-email"
    | "totp"
    | "organization"
    | "link-account"
    | "complete";
  state?: string;
  email?: string;
  secret?: string;
  organizations?: { id: string; name: string }[];
  message?: string;
  returnTo?: string;
};

export function ManagedSignIn({
  configuration,
  titleId,
  onDone,
  onInteraction,
}: {
  configuration: HostedPlatform;
  titleId: string;
  onDone: () => void;
  onInteraction?: () => void;
}) {
  const [query] = useState(() => new URLSearchParams(window.location.search));
  const [resetToken] = useState(() =>
    query.get("view") === "reset-password" ? (query.get("token") ?? "") : "",
  );
  const [invitation] = useState(() => query.get("invitation_token") ?? "");
  const linking = query.get("link") === "workos";
  const previewingProduction = !!configuration.dataOrigin;
  const methods = (
    configuration.workosLoginMethods ?? ["email-code", "password"]
  ).filter(
    (method) =>
      !previewingProduction || method === "email-code" || method === "password",
  );
  const [method, setMethod] = useState(
    methods.includes("email-code") ? "email-code" : "password",
  );
  const [step, setStep] = useState<Step>(
    resetToken ? "reset-password" : "login",
  );
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [code, setCode] = useState("");
  const [progress, setProgress] = useState<Progress | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState(() =>
    query.get("workos_return") === "1"
      ? "Enter your email to finish signing in."
      : "",
  );
  const [invitationReady, setInvitationReady] = useState(!invitation);
  const initialized = useRef(false);

  useEffect(() => {
    if (initialized.current) return;
    initialized.current = true;
    const clean = new URL(window.location.href);
    clean.searchParams.delete("invitation_token");
    clean.searchParams.delete("managed_state");
    clean.searchParams.delete("workos_return");
    clean.searchParams.delete("auth_error");
    if (resetToken) clean.searchParams.delete("token");
    window.history.replaceState(
      window.history.state,
      "",
      clean.pathname + clean.search,
    );
    if (query.get("managed_state")) {
      setBusy(true);
      void hostedRequest<Progress>("/account/workos/authenticate", "POST", {
        state: query.get("managed_state"),
        resume: true,
      })
        .then(receive)
        .catch((cause) => setError((cause as Error).message))
        .finally(() => setBusy(false));
    } else if (invitation) {
      void hostedRequest<{ email: string }>(
        "/account/workos/invitation",
        "POST",
        { token: invitation },
      )
        .then((result) => {
          setEmail(result.email);
          setInvitationReady(true);
        })
        .catch((cause) => setError((cause as Error).message));
    } else if (linking) {
      void hostedRequest<{ email: string }>("/account")
        .then((result) => setEmail(result.email))
        .catch(() =>
          setError("Sign in before connecting WorkOS to your account."),
        );
    }
  }, [invitation, linking, resetToken]);

  const changeStep = (next: Step) => {
    onInteraction?.();
    setStep(next);
    setError("");
    setNotice("");
    setPassword("");
    setConfirmation("");
    setCode("");
    setProgress(null);
  };
  const receive = (result: Progress) => {
    setPassword("");
    setCode("");
    if (result.step === "complete") {
      setProgress(null);
      if (linking || (result.returnTo && result.returnTo !== "/")) {
        const destination = new URL(
          result.returnTo || "/?view=account",
          window.location.origin,
        );
        if (destination.origin !== window.location.origin) {
          setError("Invalid sign-in destination.");
          return;
        }
        window.location.assign(destination.href);
      } else onDone();
      return;
    }
    setProgress(result);
    setStep(result.step);
    if (result.email) setEmail(result.email);
    setError(result.message ?? "");
  };
  const submit = async (event: FormEvent, organizationId?: string) => {
    event.preventDefault();
    if (busy) return;
    onInteraction?.();
    setError("");
    setNotice("");
    if (step === "reset-password" && password !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      if (step === "waitlist" || step === "forgot-password") {
        const result = await hostedRequest<{ message: string }>(
          step === "waitlist"
            ? "/account/workos/waitlist"
            : "/account/workos/password-reset",
          "POST",
          { email },
        );
        setNotice(result.message);
      } else if (step === "reset-password") {
        const result = await hostedRequest<{ message: string }>(
          "/account/workos/password-reset/confirm",
          "POST",
          { token: resetToken, password },
        );
        changeStep("login");
        setNotice(result.message);
      } else {
        const body =
          step === "login"
            ? {
                method,
                email,
                ...(method === "password" ? { password } : {}),
                ...(invitation ? { invitationToken: invitation } : {}),
                ...(linking ? { link: true } : {}),
                tenantId: query.get("tenant") ?? "",
                codeChallenge: query.get("challenge") ?? "",
              }
            : {
                state: progress?.state,
                ...(step === "link-account"
                  ? { previousPassword: password }
                  : organizationId
                    ? { organizationId }
                    : { code }),
              };
        receive(
          await hostedRequest<Progress>(
            "/account/workos/authenticate",
            "POST",
            body,
          ),
        );
      }
    } catch (cause) {
      if (progress) {
        // Terminal failures consume the continuation. Do not invite another
        // submission of a code whose state can no longer be used.
        setStep("login");
        setProgress(null);
        setPassword("");
        setCode("");
      }
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const social = async (provider: string) => {
    onInteraction?.();
    setBusy(true);
    setError("");
    try {
      await startManagedLogin(false, false, provider, email, invitation);
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };
  const titles: Record<Step, string> = {
    login: linking
      ? "Connect your account"
      : invitation
        ? "You're invited to Doodship"
        : "Sign in to Doodship",
    "email-code": "Check your email",
    "verify-email": "Verify your email",
    totp: "Two-step verification",
    organization: "Choose an organization",
    "link-account": "Connect your existing account",
    waitlist: "Join the waitlist",
    "forgot-password": "Reset your password",
    "reset-password": "Choose a new password",
  };
  const codeStep = ["email-code", "verify-email", "totp"].includes(step);
  return (
    <>
      <h1 id={titleId}>{titles[step]}</h1>
      {codeStep && (
        <p className="account-signin-description">
          {step === "totp"
            ? "Enter the code from your authenticator app."
            : `If this email can sign in, a code has been sent to ${email}.`}
        </p>
      )}
      {step === "waitlist" && (
        <p className="account-signin-description">
          Doodship is open by invitation. Request yours here.
        </p>
      )}
      {step === "link-account" && (
        <p className="account-signin-description">
          {previewingProduction
            ? "Connect your account on production, then return here to sign in."
            : "Email verified. Enter your previous Doodship password once to keep your instances and access."}
        </p>
      )}
      {linking && step === "login" && (
        <p className="account-signin-description">
          Verify your email to keep your instances and access.
        </p>
      )}
      {error && (
        <p className="form-error" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p className="form-success" role="status">
          {notice}
        </p>
      )}
      {step === "login" &&
        !linking &&
        (methods.includes("github") || methods.includes("google")) && (
          <div className="account-social-buttons">
            {methods.includes("github") && (
              <button
                className="secondary-button"
                disabled={busy || !invitationReady}
                onClick={() => void social("github")}
              >
                <GithubLogo size={19} />
                Continue with GitHub
              </button>
            )}
            {methods.includes("google") && (
              <button
                className="secondary-button"
                disabled={busy || !invitationReady}
                onClick={() => void social("google")}
              >
                <GoogleLogo size={19} />
                Continue with Google
              </button>
            )}
            <div className="account-signin-divider">
              <span>or use email</span>
            </div>
          </div>
        )}
      {step === "link-account" && previewingProduction ? (
        <a
          className="primary-button"
          href={`${configuration.dataOrigin}/?view=login`}
        >
          Connect account on production
        </a>
      ) : step === "organization" ? (
        <div className="account-organization-buttons" aria-busy={busy}>
          {progress?.organizations?.map((org) => (
            <button
              key={org.id}
              className="secondary-button"
              disabled={busy}
              onClick={(event) => void submit(event, org.id)}
            >
              {org.name}
            </button>
          ))}
        </div>
      ) : (
        <form
          className="hosted-form"
          aria-busy={busy}
          onSubmit={(event) => void submit(event)}
        >
          {(["login", "waitlist", "forgot-password"] as Step[]).includes(
            step,
          ) && (
            <label>
              Email
              <input
                type="email"
                autoComplete="username"
                autoFocus
                value={email}
                readOnly={!!invitation || linking}
                onChange={(event) => setEmail(event.target.value)}
                required
                maxLength={254}
              />
            </label>
          )}
          {step === "login" && method === "password" && (
            <PasswordField
              label="Password"
              value={password}
              onChange={setPassword}
            />
          )}
          {step === "link-account" && (
            <PasswordField
              label="Previous Doodship password"
              value={password}
              onChange={setPassword}
            />
          )}
          {step === "login" &&
            method === "password" &&
            !previewingProduction && (
              <button
                type="button"
                className="account-signin-switch"
                disabled={busy}
                onClick={() => changeStep("forgot-password")}
              >
                Forgot password?
              </button>
            )}
          {codeStep && (
            <>
              {progress?.secret && (
                <div className="account-mfa-setup">
                  <p>
                    Add this setup key to your authenticator app, then enter its
                    code.
                  </p>
                  <code>{progress.secret}</code>
                </div>
              )}
              <label>
                {step === "totp" ? "Authenticator code" : "Email code"}
                <input
                  autoFocus
                  type="text"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  value={code}
                  onChange={(event) =>
                    setCode(event.target.value.replace(/\D/g, ""))
                  }
                  required
                />
              </label>
            </>
          )}
          {step === "reset-password" && (
            <>
              <PasswordField
                label="New password"
                newPassword
                value={password}
                onChange={setPassword}
              />
              <PasswordField
                label="Confirm password"
                newPassword
                value={confirmation}
                onChange={setConfirmation}
              />
            </>
          )}
          <button
            className="primary-button"
            disabled={busy || (step === "login" && !invitationReady)}
          >
            {busy
              ? "Please wait..."
              : step === "login"
                ? method === "email-code"
                  ? "Send sign-in code"
                  : "Sign in"
                : step === "waitlist"
                  ? "Request invitation"
                  : step === "forgot-password"
                    ? "Send reset link"
                    : step === "reset-password"
                      ? "Save password"
                      : step === "link-account"
                        ? "Connect account"
                        : "Verify code"}
          </button>
        </form>
      )}
      {step === "login" &&
        methods.includes("email-code") &&
        methods.includes("password") && (
          <button
            type="button"
            className="account-signin-switch account-method-switch"
            disabled={busy}
            onClick={() => {
              setMethod(method === "email-code" ? "password" : "email-code");
              setPassword("");
              setError("");
            }}
          >
            {method === "email-code" ? "Use a password" : "Use an email code"}
          </button>
        )}
      {step === "login" && methods.includes("sso") && !linking && (
        <button
          type="button"
          className="account-signin-switch account-method-switch"
          disabled={busy || !email}
          onClick={() => void social("sso")}
        >
          Sign in with SSO
        </button>
      )}
      {step === "login" &&
        configuration.waitlistEnabled &&
        !previewingProduction &&
        !invitation &&
        !linking && (
          <p className="account-waitlist">
            Access is by invitation.{" "}
            <button
              type="button"
              className="account-signin-switch"
              disabled={busy}
              onClick={() => changeStep("waitlist")}
            >
              Join waitlist
            </button>
          </p>
        )}
      {step !== "login" && (
        <button
          type="button"
          className="account-signin-switch account-method-switch"
          disabled={busy}
          onClick={() => changeStep("login")}
        >
          {codeStep || step === "organization"
            ? "Start sign-in again"
            : "Back to sign in"}
        </button>
      )}
    </>
  );
}
