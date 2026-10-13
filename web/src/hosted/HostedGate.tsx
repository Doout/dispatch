import { useEffect, useRef, useState, type ReactNode } from "react";
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
      <main className="hosted-auth">
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
      <main className="hosted-auth" aria-busy="true">
        <Mark />
        <p role="status">Opening Doodship...</p>
      </main>
    );
  if (configuration === null) return children;
  if (configuration.mode === "platform")
    return <PlatformApp configuration={configuration} />;
  return (
    <HostedTenantContext.Provider value={configuration}>
      {children}
    </HostedTenantContext.Provider>
  );
}

export function TenantSignIn({
  loginUrl,
  name,
}: {
  loginUrl: string;
  name: string;
}) {
  const [providers, setProviders] = useState<PublicAuthProvider[]>([]);
  const [error, setError] = useState(
    () => new URLSearchParams(window.location.search).get("auth_error") ?? "",
  );
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let mounted = true;
    void import("../api")
      .then(({ api }) => mounted ? api.authProviders() : [])
      .then((providers) => { if (mounted) setProviders(providers); })
      .catch(() => { if (mounted) setError("Sign-in methods could not be loaded. Try again."); });
    return () => { mounted = false; };
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
  return (
    <main className="hosted-auth">
      <Mark />
      <h1>Sign in to {name}</h1>
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
      {error && (
        <p role="alert" className="form-error">
          {error}
        </p>
      )}
    </main>
  );
}
