import { useId, useState, type FormEvent } from "react";
import { ArrowUpRight } from "@phosphor-icons/react";
import { hostedRequest, type HostedPlatform } from "./client";
import { HostedBrand } from "./HostedChrome";
import "./AccountForm.css";
import { PasswordField } from "./PasswordField";
import { ManagedSignIn } from "./ManagedSignIn";

export function AccountForm({
  configuration,
  mode: initialMode,
  verification,
  onDone,
  initialError = "",
}: {
  configuration: HostedPlatform;
  mode: "login" | "verify";
  verification?: string;
  onDone: () => void;
  initialError?: string;
}) {
  const titleId = useId();
  const [mode, setMode] = useState<"login" | "register" | "verify">(
      initialMode,
    ),
    [email, setEmail] = useState(""),
    [name, setName] = useState(""),
    [password, setPassword] = useState(""),
    [confirmation, setConfirmation] = useState(""),
    [error, setError] = useState(initialError),
    [notice, setNotice] = useState(""),
    [busy, setBusy] = useState(false);
  const landingEnabled =
    import.meta.env.DEV || import.meta.env.VITE_HOSTED_LANDING_PAGE === "true";
  const localLogin = configuration.passwordLoginEnabled !== false;
  const managedSignIn = configuration.authkitEnabled;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (busy) return;
    setError("");
    setNotice("");
    if (mode === "verify" && password !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      if (mode === "login") {
        await hostedRequest("/account/login", "POST", { email, password });
        onDone();
      } else if (mode === "register") {
        await hostedRequest("/account/register", "POST", { email, name });
        setNotice("Check your email to finish creating your account.");
      } else {
        await hostedRequest("/account/verify-email", "POST", {
          token: verification,
          name,
          password,
        });
        onDone();
      }
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="hosted-auth-page account-signin">
      <a className="skip-link" href="#account-signin-content">
        Skip to sign in
      </a>
      <aside className="account-signin-aside" aria-label="Doodship">
        <HostedBrand />
        <div className="account-signin-illustration" aria-hidden="true">
          <picture>
            <source
              media="(min-width: 768px)"
              srcSet="/brand/doodship-courier-256.webp 256w, /brand/doodship-courier-512.webp 512w"
              sizes="(min-width: 1024px) 320px, 256px"
            />
            <img
              src="/brand/doodship-icon-32.png"
              width={320}
              height={320}
              alt=""
              decoding="async"
            />
          </picture>
        </div>
      </aside>
      <div className="account-signin-content">
        <header className="account-signin-header">
          <div className="account-signin-mobile-brand">
            <HostedBrand />
          </div>
          {landingEnabled && (
            <a className="hosted-text-link" href="/">
              Back to home <ArrowUpRight size={15} aria-hidden="true" />
            </a>
          )}
        </header>
        <section
          id="account-signin-content"
          className="account-signin-form"
          aria-labelledby={titleId}
          tabIndex={-1}
        >
          {!managedSignIn && (
            <h1 id={titleId}>
              {mode === "login"
                ? "Sign in to Doodship"
                : mode === "register"
                  ? "Create your account"
                  : "Finish creating your account"}
            </h1>
          )}
          {!managedSignIn && mode !== "login" && (
            <p className="account-signin-description">
              {mode === "register"
                ? "We'll email you a link to finish setting up your account."
                : "Set a name and password for your account."}
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
          {managedSignIn && (
            <ManagedSignIn
              configuration={configuration}
              titleId={titleId}
              onDone={onDone}
              onInteraction={() => setError("")}
            />
          )}
          {localLogin && !managedSignIn && (
            <form
              className="hosted-form"
              aria-busy={busy}
              onSubmit={(event) => void submit(event)}
            >
              {mode !== "verify" && (
                <label>
                  Email
                  <input
                    type="email"
                    autoComplete="username"
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                    autoFocus={!managedSignIn}
                    required
                  />
                </label>
              )}
              {mode !== "login" && (
                <label>
                  Name
                  <input
                    autoComplete="name"
                    value={name}
                    onChange={(event) => setName(event.target.value)}
                    autoFocus={mode === "verify"}
                    required
                    maxLength={120}
                  />
                </label>
              )}
              {mode !== "register" && (
                <PasswordField
                  key={mode}
                  label={mode === "verify" ? "Choose a password" : "Password"}
                  value={password}
                  onChange={setPassword}
                  newPassword={mode === "verify"}
                />
              )}
              {mode === "verify" && (
                <PasswordField
                  label="Confirm password"
                  value={confirmation}
                  onChange={setConfirmation}
                  newPassword
                />
              )}
              <button type="submit" className="primary-button" disabled={busy}>
                {busy
                  ? mode === "login"
                    ? "Signing in..."
                    : mode === "register"
                      ? "Sending email..."
                      : "Creating account..."
                  : mode === "login"
                    ? "Sign in"
                    : mode === "register"
                      ? "Send verification email"
                      : "Create account"}
              </button>
              {configuration.registrationEnabled && mode !== "verify" && (
                <button
                  type="button"
                  className="account-signin-switch"
                  disabled={busy}
                  onClick={() => {
                    setMode(mode === "login" ? "register" : "login");
                    setError("");
                    setNotice("");
                    setPassword("");
                    setConfirmation("");
                  }}
                >
                  {mode === "login" ? "Create an account" : "Back to sign in"}
                </button>
              )}
            </form>
          )}
        </section>
      </div>
    </main>
  );
}
