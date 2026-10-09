import { useEffect, useState } from "react";
import { getImpersonatedUserID, getToken } from "../api";

export type WorkflowJobLog = { id: string; name: string; state: string; error: string; text: string };
type LogDelta = WorkflowJobLog & { keep: number };
type LogState = {
  scope: string;
  jobs: Record<string, WorkflowJobLog>;
  phase: "idle" | "connecting" | "live" | "complete" | "error";
  status: string;
  started: boolean;
};

function emptyState(scope: string): LogState {
  return { scope, jobs: {}, phase: "idle", status: "Saved output", started: false };
}

// Starting a stream keeps it open through its final events, even when a metadata
// poll sees the completed revision first. Each new connection replays saved logs.
export function useWorkflowLogs(revisionID: string, start = true, scope = revisionID) {
  const [state, setState] = useState<LogState>(() => emptyState(scope));
  const [attempt, setAttempt] = useState(0);
  const current = state.scope === scope ? state : emptyState(scope);
  const watch = Boolean(revisionID) && (start || current.started);

  useEffect(() => {
    if (!watch) {
      setState(emptyState(scope));
      return;
    }
    const controller = new AbortController();
    setState(previous => ({
      ...emptyState(scope),
      jobs: previous.scope === scope ? previous.jobs : {},
      phase: "connecting", status: "Connecting…", started: true,
    }));
    async function connect() {
      try {
        const impersonated = getImpersonatedUserID();
        const response = await fetch(`/api/v1/workflow/revisions/${encodeURIComponent(revisionID)}/logs/watch`, {
          headers: { Accept: "text/event-stream", ...(getToken() ? { Authorization: `Bearer ${getToken()}` } : {}), ...(impersonated ? { "Impersonate-User": impersonated } : {}) },
          signal: controller.signal, cache: "no-store",
        });
        if (!response.ok || !response.body || !response.headers.get("content-type")?.includes("text/event-stream")) throw new Error(`Cannot open build logs (${response.status}).`);
        const reader = response.body.getReader();
        const cancelReader = () => { void reader.cancel().catch(() => {}); };
        controller.signal.addEventListener("abort", cancelReader, { once: true });
        const decoder = new TextDecoder();
        let buffer = "";
        try {
          while (!controller.signal.aborted) {
            const { value, done } = await reader.read();
            if (controller.signal.aborted) return;
            if (done) throw new Error("Log connection closed.");
            buffer += decoder.decode(value, { stream: true });
            let boundary: number;
            while ((boundary = buffer.indexOf("\n\n")) >= 0) {
              const message = buffer.slice(0, boundary);
              buffer = buffer.slice(boundary + 2);
              const lines = message.split("\n");
              const event = lines.find(line => line.startsWith("event: "))?.slice(7);
              const data = lines.filter(line => line.startsWith("data: ")).map(line => line.slice(6)).join("\n");
              if (event === "connected") setState(previous => ({ ...previous, phase: "live", status: "Live" }));
              if (event === "log") {
                const delta: LogDelta = JSON.parse(data);
                setState(previous => ({ ...previous, jobs: {
                  ...previous.jobs,
                  [delta.id]: { ...delta, text: (previous.jobs[delta.id]?.text ?? "").slice(0, delta.keep) + delta.text },
                } }));
              }
              if (event === "complete") {
                const status = `Run ${JSON.parse(data)}`;
                setState(previous => ({ ...previous, phase: "complete", status }));
                return;
              }
              if (event === "auth-error") throw new Error("Access expired. Sign in again to view logs.");
              if (event === "error") throw new Error(JSON.parse(data));
            }
          }
        } finally {
          controller.signal.removeEventListener("abort", cancelReader);
          await reader.cancel().catch(() => {});
        }
      } catch (error) {
        if (!controller.signal.aborted) setState(previous => ({ ...previous, phase: "error", status: error instanceof Error ? error.message : "Log connection failed." }));
      }
    }
    void connect();
    return () => controller.abort();
  }, [revisionID, scope, watch, attempt]);

  return { ...current, reconnect: () => setAttempt(value => value + 1) };
}
