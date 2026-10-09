import { FormEvent, useCallback, useEffect, useState } from "react";
import { ArrowLeft, GithubLogo, Key } from "@phosphor-icons/react";
import { api, PublicAuthProvider, setToken } from "../api";
import { Mark } from "../components/PageStates";
import { useHostedTenant } from "../hosted/context";
import { TenantSignIn } from "../hosted/HostedGate";

export function AuthScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const hosted = useHostedTenant();
  if (hosted) return <TenantSignIn name={hosted.tenant.name} loginUrl={`${hosted.origin}/api/v1/hosted/auth/start`} />;
  return <LocalAuthScreen onAuthenticated={onAuthenticated} />;
}

function LocalAuthScreen({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [setupRequired, setSetupRequired] = useState<boolean | null>(null);
  const [providers, setProviders] = useState<PublicAuthProvider[]>([]);
  const [phase, setPhase] = useState<"identify" | "password" | "choose">(
    "identify",
  );
  const [passwordAvailable, setPasswordAvailable] = useState(true);
  const [identifier, setIdentifier] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const loadStatus = useCallback(async (clearError = true) => {
    setSetupRequired(null);
    if (clearError) setError("");
    try {
      const [status, methods] = await Promise.all([
        api.authStatus(),
        api.authProviders(),
      ]);
      setSetupRequired(status.setupRequired);
      setProviders(methods);
      const query = new URLSearchParams(window.location.search);
      const requestedProviderID = query.get("authProvider");
      if (requestedProviderID && !status.setupRequired) {
        query.delete("authProvider");
        window.history.replaceState(
          {},
          "",
          `${window.location.pathname}${query.size ? `?${query}` : ""}${window.location.hash}`,
        );
        const requestedProvider = methods.find(
          (method) => method.id === requestedProviderID,
        );
        if (!requestedProvider) {
          setPhase("choose");
          setError("This sign-in method is no longer available.");
          return;
        }
        setBusy(true);
        const started = await api.startOAuth(requestedProvider.id);
        window.location.assign(started.authorizationUrl);
        return;
      }
      if (!status.setupRequired && methods.length === 0) setPhase("password");
    } catch {
      setError("Unable to reach the controller.");
    }
  }, []);
  useEffect(() => {
    const query = new URLSearchParams(window.location.search);
    const code = query.get("auth_code");
    const oauthError = query.get("auth_error");
    if (!code && !oauthError) {
      void loadStatus();
      return;
    }
    query.delete("auth_code");
    query.delete("auth_error");
    window.history.replaceState(
      {},
      "",
      `${window.location.pathname}${query.size ? `?${query}` : ""}${window.location.hash}`,
    );
    if (oauthError) {
      setError(oauthError);
      void loadStatus(false);
      return;
    }
    setBusy(true);
    void api
      .exchangeOAuth(code ?? "")
      .then(async (session) => {
        setToken(session.token);
        await api.overview();
        onAuthenticated();
      })
      .catch((cause) => {
        setError((cause as Error).message);
        void loadStatus(false);
      })
      .finally(() => setBusy(false));
  }, [loadStatus]);

  async function continueWithIdentifier(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const discovery = await api.discoverAuth(identifier);
      setPasswordAvailable(discovery.password);
      if (discovery.method === "provider" && discovery.provider)
        await beginOAuth(discovery.provider);
      else setPhase(discovery.method === "password" ? "password" : "choose");
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function beginOAuth(provider: PublicAuthProvider) {
    setBusy(true);
    setError("");
    try {
      const started = await api.startOAuth(provider.id);
      window.location.assign(started.authorizationUrl);
    } catch (cause) {
      setBusy(false);
      setError((cause as Error).message);
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    setError("");
    if (setupRequired && password !== confirmation) {
      setError("Passwords do not match.");
      return;
    }
    setBusy(true);
    try {
      if (setupRequired) await api.setupAdmin(identifier, password);
      const session = await api.login(identifier, password);
      setToken(session.token);
      await api.overview();
      onAuthenticated();
    } catch (cause) {
      setToken("");
      const failure = cause as Error & { status?: number };
      if (failure.status === 409) {
        setSetupRequired(false);
        setError("Administrator already exists. Sign in instead.");
      } else if (failure.status === 429) {
        setError("Too many sign-in attempts. Try again later.");
      } else
        setError(
          setupRequired ? failure.message : "Incorrect username or password.",
        );
    } finally {
      setBusy(false);
    }
  }

  const back = () => {
    setPhase(providers.length ? "identify" : "password");
    setPassword("");
    setError("");
  };
  return (
    <main className="auth-screen">
      <section className="auth-card" aria-labelledby="auth-title">
        <div className="auth-brand">
          <Mark />
          <strong>Dispatch</strong>
        </div>
        {setupRequired === null ? (
          error ? (
            <div className="auth-connection-error">
              <h1 id="auth-title">Controller unavailable</h1>
              <p>{error}</p>
              <button
                className="quiet-button"
                onClick={() => void loadStatus()}
              >
                Retry
              </button>
            </div>
          ) : (
            <div className="auth-loading" aria-label="Connecting">
              <span />
              <span />
              <span />
            </div>
          )
        ) : (
          <>
            <header>
              <h1 id="auth-title">
                {setupRequired ? "Create administrator" : "Sign in"}
              </h1>
              {!setupRequired && phase === "identify" && (
                <p>Use your work email or username.</p>
              )}
            </header>
            {setupRequired ? (
              <form onSubmit={submit} aria-busy={busy}>
                <label>
                  <span>Username</span>
                  <input
                    value={identifier}
                    onChange={(event) => setIdentifier(event.target.value)}
                    autoComplete="username"
                    autoFocus
                    required
                    minLength={3}
                    maxLength={64}
                    disabled={busy}
                  />
                </label>
                <label>
                  <span>Password</span>
                  <input
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    autoComplete="new-password"
                    required
                    minLength={12}
                    disabled={busy}
                  />
                  <small>Use at least 12 characters.</small>
                </label>
                <label>
                  <span>Confirm password</span>
                  <input
                    type="password"
                    value={confirmation}
                    onChange={(event) => setConfirmation(event.target.value)}
                    autoComplete="new-password"
                    required
                    minLength={12}
                    disabled={busy}
                  />
                </label>
                {error && (
                  <p className="auth-error" role="alert">
                    {error}
                  </p>
                )}
                <button className="primary-button" disabled={busy}>
                  {busy ? "Creating..." : "Create account"}
                </button>
              </form>
            ) : phase === "identify" ? (
              <form onSubmit={continueWithIdentifier} aria-busy={busy}>
                <label>
                  <span>Email or username</span>
                  <input
                    value={identifier}
                    onChange={(event) => setIdentifier(event.target.value)}
                    autoComplete="username"
                    autoFocus
                    required
                    disabled={busy}
                  />
                </label>
                {error && (
                  <p className="auth-error" role="alert">
                    {error}
                  </p>
                )}
                <button className="primary-button" disabled={busy}>
                  {busy ? "Checking..." : "Continue"}
                </button>
                <button
                  type="button"
                  className="auth-method-link"
              onClick={() => {
                setPhase("choose");
                setPasswordAvailable(true);
                setError("");
                  }}
                >
                  Use another sign-in method
                </button>
              </form>
            ) : phase === "password" ? (
              <form onSubmit={submit} aria-busy={busy}>
                {providers.length > 0 && (
                  <button type="button" className="auth-back" onClick={back}>
                    <ArrowLeft size={15} />
                    Back
                  </button>
                )}
                <label>
                  <span>Email or username</span>
                  <input
                    value={identifier}
                    onChange={(event) => setIdentifier(event.target.value)}
                    autoComplete="username"
                    required
                    autoFocus={!identifier}
                    disabled={busy}
                  />
                </label>
                <label>
                  <span>Password</span>
                  <input
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    autoComplete="current-password"
                    required
                    autoFocus={Boolean(identifier)}
                    disabled={busy}
                  />
                </label>
                {error && (
                  <p className="auth-error" role="alert">
                    {error}
                  </p>
                )}
                <button className="primary-button" disabled={busy}>
                  {busy ? "Signing in..." : "Sign in"}
                </button>
                {providers.length > 0 && (
                  <button
                    type="button"
                    className="auth-method-link"
              onClick={() => setPhase("choose")}
                  >
                    Use another sign-in method
                  </button>
                )}
              </form>
            ) : (
              <div className="auth-methods">
                <button type="button" className="auth-back" onClick={back}>
                  <ArrowLeft size={15} />
                  Back
                </button>
                {providers.map((provider) => (
                  <button
                    type="button"
                    className="auth-provider"
                    key={provider.id}
                    onClick={() => void beginOAuth(provider)}
                    disabled={busy}
                  >
                    <GithubLogo size={19} weight="fill" />
                    <span>
                      <strong>{provider.name}</strong>
                      <small>{new URL(provider.baseUrl).host}</small>
                    </span>
                  </button>
                ))}
          {passwordAvailable && (
            <button
              type="button"
              className="auth-provider"
              onClick={() => setPhase("password")}
              disabled={busy}
            >
              <Key size={19} />
              <span>
                <strong>Local account</strong>
                <small>Dispatch password</small>
              </span>
            </button>
          )}
                {error && (
                  <p className="auth-error" role="alert">
                    {error}
                  </p>
                )}
              </div>
            )}
          </>
        )}
      </section>
    </main>
  );
}
