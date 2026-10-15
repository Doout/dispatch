import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import { setToken } from "../api/transport";
import type { PublicAuthProvider } from "../api";
import { Mark } from "../components/PageStates";
import {
  detectHosted,
  hostedRequest,
  type HostedConfiguration,
} from "./client";
import { HostedTenantContext } from "./context";
import { PlatformApp } from "./PlatformApp";
import { HostedBrand } from "./HostedChrome";
import "./InstanceSignIn.css";
import "./hosted.css";

export function HostedGate({ children }: { children: ReactNode }) {
  const [configuration, setConfiguration] = useState<
    HostedConfiguration | null | undefined
  >();
  const [error, setError] = useState("");
  const [attempt, setAttempt] = useState(0);
  const exchange = useRef<Promise<void> | null>(null);
  useEffect(() => {
    let mounted = true;
    void (async () => {
      try {
        const value = await detectHosted();
        if (!mounted) return;
        if (value) setToken("");
        if (value?.mode === "tenant") {
          const fragment = new URLSearchParams(window.location.hash.slice(1));
          const code = fragment.get("tenant_code");
          if (code && !exchange.current) {
            window.history.replaceState(
              window.history.state,
              "",
              `${window.location.pathname}${window.location.search}`,
            );
            exchange.current = hostedRequest<void>(
              "/hosted/auth/exchange",
              "POST",
              { code },
            );
          }
          if (exchange.current) await exchange.current;
        }
        if (mounted) {
          setConfiguration(value);
          setError("");
        }
      } catch (cause) {
        if (mounted) setError((cause as Error).message);
      }
    })();
    return () => {
      mounted = false;
    };
  }, [attempt]);
  if (error)
    return (
      <main className="hosted-auth-page instance-signin-state">
        <Mark />
        <h1>Unable to open Doodship</h1>
        <p role="alert">{error}</p>
        <button
          className="primary-button"
          onClick={() => {
            exchange.current = null;
            setError("");
            setAttempt((v) => v + 1);
          }}
        >
          Try again
        </button>
      </main>
    );
  if (configuration === undefined)
    return (
      <main className="hosted-auth-page instance-signin-state" aria-busy="true">
        <Mark />
        <p role="status">Opening Doodship...</p>
      </main>
    );
  if (configuration === null) return children;
  if (configuration.mode === "platform")
    return <PlatformApp configuration={configuration} />;
  return (
    <HostedTenantContext.Provider
      value={{
        ...configuration,
        refreshAuthentication: async () => {
          const value = await detectHosted();
          if (value?.mode !== "tenant")
            throw new Error("Instance sign-in is unavailable. Try again.");
          setConfiguration(value);
        },
      }}
    >
      {children}
    </HostedTenantContext.Provider>
  );
}

export function TenantSignIn({
  loginUrl,
  name,
  nativeSignInAvailable = false,
  onAuthenticated,
}: {
  loginUrl: string;
  name: string;
  nativeSignInAvailable?: boolean;
  onAuthenticated: () => void | Promise<void>;
}) {
  const titleId = useId();
  const [providers, setProviders] = useState<PublicAuthProvider[]>([]);
  const [error, setError] = useState(
    () => new URLSearchParams(window.location.search).get("auth_error") ?? "",
  );
  const [busy, setBusy] = useState(false);
  const [passwordForm, setPasswordForm] = useState(false);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const nativeExchange = useRef<Promise<void> | null>(null);
  const authenticated = useRef(onAuthenticated);
  authenticated.current = onAuthenticated;
  useEffect(() => {
    let mounted = true;
    const query = new URLSearchParams(window.location.search);
    const code = query.get("auth_code");
    if (!nativeExchange.current && code && nativeSignInAvailable) {
      query.delete("auth_code");
      window.history.replaceState(
        window.history.state,
        "",
        `${window.location.pathname}${query.size ? `?${query}` : ""}${window.location.hash}`,
      );
      nativeExchange.current = import("../api").then(async ({ api }) => {
        await api.exchangeOAuth(code);
      });
    }
    if (nativeExchange.current) {
      setBusy(true);
      void nativeExchange.current
        .then(async () => {
          if (mounted) await authenticated.current();
        })
        .catch((cause) => {
          if (mounted) setError((cause as Error).message);
        })
        .finally(() => {
          if (mounted) setBusy(false);
        });
    }
    return () => {
      mounted = false;
    };
  }, [nativeSignInAvailable]);
  useEffect(() => {
    let mounted = true;
    void import("../api")
      .then(({ api }) => (mounted ? api.authProviders() : []))
      .then((providers) => {
        if (mounted) setProviders(providers);
      })
      .catch(() => {
        if (mounted)
          setError("Sign-in methods could not be loaded. Try again.");
      });
    return () => {
      mounted = false;
    };
  }, []);
  const signIn = async (id: string) => {
    setBusy(true);
    setError("");
    try {
      const { api } = await import("../api");
      const result = await api.startOAuth(id);
      window.location.assign(result.authorizationUrl);
    } catch (cause) {
      setError((cause as Error).message);
      setBusy(false);
    }
  };
  const passwordSignIn = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const { api } = await import("../api");
      await api.login(username.trim(), password);
      setPassword("");
      await authenticated.current();
    } catch (cause) {
      const failure = cause as Error & { status?: number };
      setError(
        failure.status === 401
          ? "Incorrect username or password."
          : failure.message,
      );
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="hosted-auth-page instance-signin">
      <aside className="instance-signin-aside" aria-label="Doodship">
        <HostedBrand />
        <div className="instance-signin-art" aria-hidden="true">
          <img
            src="/brand/doodship-courier-512.webp"
            srcSet="/brand/doodship-courier-256.webp 256w, /brand/doodship-courier-512.webp 512w"
            sizes="(max-width: 767px) 0px, 32vw"
            width={512}
            height={512}
            alt=""
          />
        </div>
      </aside>
      <div className="instance-signin-content">
        <div className="instance-signin-mobile-brand">
          <HostedBrand />
        </div>
        <section className="instance-signin-form" aria-labelledby={titleId}>
          <h1 id={titleId}>Sign in to {name}</h1>
          <div className="instance-signin-methods">
            <a className="primary-button" href={loginUrl}>
              Sign in with Doodship
            </a>
            {providers.map((provider) => (
              <button
                key={provider.id}
                className="quiet-button"
                disabled={busy}
                onClick={() => void signIn(provider.id)}
              >
                Sign in with {provider.name}
              </button>
            ))}
          </div>
          {nativeSignInAvailable &&
            (!passwordForm ? (
              <button
                type="button"
                className="quiet-button instance-signin-password"
                disabled={busy}
                onClick={() => setPasswordForm(true)}
              >
                Use instance password
              </button>
            ) : (
              <form onSubmit={(event) => void passwordSignIn(event)}>
                <div className="instance-signin-divider" aria-hidden="true">
                  or
                </div>
                <label>
                  Username or email
                  <input
                    autoComplete="username"
                    required
                    value={username}
                    disabled={busy}
                    onChange={(event) => setUsername(event.target.value)}
                  />
                </label>
                <label>
                  Password
                  <input
                    type="password"
                    autoComplete="current-password"
                    required
                    value={password}
                    disabled={busy}
                    onChange={(event) => setPassword(event.target.value)}
                  />
                </label>
                <button className="primary-button" disabled={busy}>
                  {busy ? "Signing in..." : "Sign in"}
                </button>
                <button
                  className="instance-signin-back"
                  type="button"
                  disabled={busy}
                  onClick={() => {
                    setPasswordForm(false);
                    setPassword("");
                  }}
                >
                  Back
                </button>
              </form>
            ))}
          {error && (
            <p role="alert" className="form-error">
              {error}
            </p>
          )}
        </section>
      </div>
    </main>
  );
}
