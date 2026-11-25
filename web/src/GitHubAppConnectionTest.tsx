import { useEffect, useRef, useState } from "react";
import { CheckCircle, PlugsConnected, WarningCircle } from "@phosphor-icons/react";
import { api, type GitHubAppNetworkSettings } from "./api";

type TestResult = { state: "checking" | "success" | "error"; message: string };

export function GitHubAppConnectionTest({ settings, busy, needsProxyURL }: {
  settings: GitHubAppNetworkSettings;
  busy: boolean;
  needsProxyURL: boolean;
}) {
  const [result, setResult] = useState<TestResult | null>(null);
  const active = useRef<AbortController | null>(null);
  useEffect(() => {
    setResult(null);
    return () => active.current?.abort();
  }, [settings]);

  async function test() {
    if (needsProxyURL) {
      setResult({ state: "error", message: "Enter the proxy URL to test this connection." });
      return;
    }
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setResult({ state: "checking", message: "Contacting the GitHub API…" });
    try {
      const response = await api.testGitHubAppConnection(settings, controller.signal);
      if (!response?.reachable) throw new Error("The controller could not confirm GitHub API connectivity.");
      if (!controller.signal.aborted) setResult({ state: "success", message: "The controller reached the GitHub API." });
    } catch (cause) {
      if (controller.signal.aborted) return;
      const error = cause as Error & { status?: number };
      const unavailable = error.status === 405 || (error.status === 404 && !/GitHub App/i.test(error.message));
      const message = unavailable
        ? "Update the connected controller to enable connection testing."
        : error.message || "The connection test failed.";
      setResult({ state: "error", message });
    }
  }

  return <div className="github-app-test">
    <button type="button" className="quiet-button" disabled={busy || result?.state === "checking"} onClick={() => void test()}>
      <PlugsConnected size={16} aria-hidden="true" />Test connection
    </button>
    {result && <span className={`github-app-test-${result.state}`} role={result.state === "error" ? "alert" : "status"}>
      {result.state === "success" && <CheckCircle size={16} aria-hidden="true" />}
      {result.state === "error" && <WarningCircle size={16} aria-hidden="true" />}
      {result.message}
    </span>}
  </div>;
}
