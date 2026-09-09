import { applyOverviewDelta, getOverviewState, setOverviewState } from "./overviewState";
import { api, getToken, getImpersonatedUserID, type Overview } from "./api";

// One connection per visible page. Fetch preserves bearer and impersonation headers.
export function subscribeOverview(onOverview: (value: Overview) => void, onAuthError: () => void) {
  let controller: AbortController | undefined;
  let retry: ReturnType<typeof setTimeout> | undefined;
  let delay = 1000;
  let stopped = false;
  const disconnect = () => {
    controller?.abort();
    clearTimeout(retry);
  };
  const connect = async () => {
    disconnect();
    if (stopped || document.hidden || !getToken() || !getOverviewState()) return;
    let baseline = getOverviewState()!;
    const current = new AbortController();
    controller = current;
    let unauthorized = false;
    const resync = async () => {
      try { await api.overview(); }
      catch (cause) {
        const status = (cause as Error & { status?: number }).status;
        if (status && [401, 403, 404, 409].includes(status)) {
          unauthorized = true;
          onAuthError();
        } else throw cause;
      }
    };
    try {
      const impersonated = getImpersonatedUserID();
      const response = await fetch("/api/v1/overview/watch", {
        headers: {
          Accept: "text/event-stream",
          "Last-Event-ID": baseline.version,
          Authorization: `Bearer ${getToken()}`,
          ...(impersonated ? { "Impersonate-User": impersonated } : {}),
        },
        signal: current.signal,
        cache: "no-store",
      });
      if (response.status === 409) {
        // Evicted baselines or a server restart require a single ordinary fetch.
        await resync();
        return;
      }
      if ([401, 403, 404].includes(response.status)) {
        unauthorized = true;
        onAuthError();
        return;
      }
      if (!response.ok || !response.body || !response.headers.get("content-type")?.includes("text/event-stream")) throw new Error("Stream unavailable");
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      try {
        while (!current.signal.aborted) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let boundary: number;
          while ((boundary = buffer.indexOf("\n\n")) >= 0) {
            const message = buffer.slice(0, boundary);
            buffer = buffer.slice(boundary + 2);
            if (current.signal.aborted) return;
            const lines = message.split("\n");
            const event = lines.find(line => line.startsWith("event: "))?.slice(7);
            const data = lines.filter(line => line.startsWith("data: ")).map(line => line.slice(6)).join("\n");
            if (event === "patch") {
              try { baseline = applyOverviewDelta(baseline, JSON.parse(data)); }
              catch { await resync(); return; }
              setOverviewState(baseline, false);
              onOverview(baseline.value);
              delay = 1000;
            }
            if (message.startsWith(":")) delay = 1000;
            if (event === "error") throw new Error("Stream unavailable");
            if (event === "auth-error") { unauthorized = true; onAuthError(); return; }
          }
        }
      } finally { await reader.cancel().catch(() => {}); }
    } catch { /* Retry connection failures without another overview polling loop. */ }
    finally {
      if (!unauthorized && !current.signal.aborted && !stopped && !document.hidden) {
        retry = setTimeout(() => void connect(), delay + Math.random() * 500);
        delay = Math.min(delay * 2, 30000);
      }
    }
  };
  const restart = () => { delay = 1000; void connect(); };
  const refreshBaseline = () => {
    const state = getOverviewState();
    if (state) onOverview(state.value);
    restart();
  };
  window.addEventListener("dispatch-overview-baseline", refreshBaseline);
  document.addEventListener("visibilitychange", restart);
  window.addEventListener("dispatch-auth-change", restart);
  void connect();
  return () => {
    stopped = true;
    disconnect();
    document.removeEventListener("visibilitychange", restart);
    window.removeEventListener("dispatch-auth-change", restart);
    window.removeEventListener("dispatch-overview-baseline", refreshBaseline);
  };
}
