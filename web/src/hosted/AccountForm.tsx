import { useState, type FormEvent } from "react";
import { ArrowUpRight } from "@phosphor-icons/react";
import { hostedRequest, type HostedPlatform } from "./client";
import { HostedBrand } from "./HostedChrome";

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
  const submit = async (event: FormEvent) => {
    event.preventDefault();
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
    <main className="hosted-auth-page">
      <header className="hosted-auth-header">
        <HostedBrand />
        <a className="hosted-text-link" href="/">
          Back to home <ArrowUpRight size={15} aria-hidden="true" />
        </a>
      </header>
      <section className="hosted-auth-card">
        <h1>
          {mode === "login"
            ? "Sign in to Dispatch"
            : mode === "register"
              ? "Create your account"
              : "Finish creating your account"}
        </h1>
        <p className="hosted-auth-description">
          {mode === "login"
            ? "Use your Dispatch account to open a tenant."
            : mode === "register"
              ? "We'll email you a link to finish setting up your account."
              : "Set a name and password for your account."}
        </p>
        <form className="hosted-form" onSubmit={(event) => void submit(event)}>
          {mode !== "verify" && (
            <label>
              Email
              <input
                type="email"
                autoComplete="username"
                value={email}
                onChange={(event) => setEmail(event.target.value)}
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
                required
                maxLength={120}
              />
            </label>
          )}
          {mode !== "register" && (
            <label>
              {mode === "verify" ? "Choose a password" : "Password"}
              <input
                type="password"
                autoComplete={
                  mode === "login" ? "current-password" : "new-password"
                }
                value={password}
                minLength={mode === "verify" ? 12 : undefined}
                onChange={(event) => setPassword(event.target.value)}
                required
              />
            </label>
          )}
          {mode === "verify" && (
            <label>
              Confirm password
              <input
                type="password"
                autoComplete="new-password"
                value={confirmation}
                minLength={12}
                onChange={(event) => setConfirmation(event.target.value)}
                required
              />
            </label>
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
          <button className="primary-button" disabled={busy}>
            {busy
              ? "Please wait..."
              : mode === "login"
                ? "Sign in"
                : mode === "register"
                  ? "Send verification email"
                  : "Create account"}
          </button>
          {configuration.registrationEnabled && mode !== "verify" && (
            <button
              type="button"
              className="quiet-button"
              onClick={() => {
                setMode(mode === "login" ? "register" : "login");
                setError("");
                setNotice("");
                setPassword("");
              }}
            >
              {mode === "login" ? "Create an account" : "Back to sign in"}
            </button>
          )}
        </form>
      </section>
    </main>
  );
}
