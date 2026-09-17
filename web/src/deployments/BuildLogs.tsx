import { useEffect, useRef, useState } from "react";
import { getImpersonatedUserID, getToken } from "../api";

type JobLog = { id: string; name: string; state: string; error: string; text: string };
type LogDelta = JobLog & { keep: number };

export function BuildLogs({ revisionID }: { revisionID: string }) {
  const [open, setOpen] = useState(false);
  return <details className="build-logs" onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>Build logs</summary>
    {open && <LiveBuildLogs key={revisionID} revisionID={revisionID} />}
  </details>;
}

function LiveBuildLogs({ revisionID }: { revisionID: string }) {
  const [follow, setFollow] = useState(true);
  const [jobs, setJobs] = useState<JobLog[]>([]);
  const [status, setStatus] = useState("Connecting…");
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setJobs([]);
    setStatus("Connecting…");
    async function watch() {
      try {
        const impersonated = getImpersonatedUserID();
        const response = await fetch(`/api/v1/workflow/revisions/${encodeURIComponent(revisionID)}/logs/watch`, {
          headers: { Accept: "text/event-stream", Authorization: `Bearer ${getToken()}`, ...(impersonated ? { "Impersonate-User": impersonated } : {}) },
          signal: controller.signal, cache: "no-store",
        });
        if (!response.ok || !response.body) throw new Error(`Cannot open build logs (${response.status}).`);
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        try {
          while (!controller.signal.aborted) {
            const { value, done } = await reader.read();
            if (done) throw new Error("Log connection closed.");
            buffer += decoder.decode(value, { stream: true });
            let boundary: number;
            while ((boundary = buffer.indexOf("\n\n")) >= 0) {
              const message = buffer.slice(0, boundary); buffer = buffer.slice(boundary + 2);
              const event = message.split("\n").find(line => line.startsWith("event: "))?.slice(7);
              const data = message.split("\n").filter(line => line.startsWith("data: ")).map(line => line.slice(6)).join("\n");
              if (event === "connected") setStatus("Live");
              if (event === "log") {
                const delta: LogDelta = JSON.parse(data);
                setJobs(previous => {
                  const before = previous.find(job => job.id === delta.id);
                  const next = { ...delta, text: (before?.text ?? "").slice(0, delta.keep) + delta.text };
                  return before ? previous.map(job => job.id === delta.id ? next : job) : [...previous, next];
                });
              }
              if (event === "complete") { setStatus(`Run ${JSON.parse(data)}`); return; }
              if (event === "auth-error") throw new Error("Access expired. Sign in again to view logs.");
              if (event === "error") throw new Error(JSON.parse(data));
            }
          }
        } finally { await reader.cancel().catch(() => {}); }
      } catch (error) {
        if (!controller.signal.aborted) setStatus(error instanceof Error ? error.message : "Log connection failed.");
      }
    }
    void watch();
    return () => controller.abort();
  }, [revisionID, attempt]);
  return <div>
    <div className="build-log-status"><span role="status">{status}</span><label><input type="checkbox" checked={follow} onChange={event => setFollow(event.target.checked)} /> Follow output</label>{status !== "Live" && !status.startsWith("Run ") && status !== "Connecting…" && <button type="button" onClick={() => setAttempt(value => value + 1)}>Reconnect</button>}</div>
    {jobs.length === 0 && <p>Waiting for a build job to start. It may be checking sources or waiting for a shared build.</p>}
    {jobs.map(job => <section key={job.id} aria-label={`${job.name} logs`}>
      <header><strong>{job.name}</strong><span>{job.state}</span></header>
      {job.error && <p role="alert">{job.error}</p>}
      <LogOutput text={job.text} follow={follow} />
    </section>)}
  </div>;
}


function LogOutput({ text, follow }: { text: string; follow: boolean }) {
  const element = useRef<HTMLPreElement>(null);
  useEffect(() => {
    if (follow && element.current) element.current.scrollTop = element.current.scrollHeight;
  }, [text, follow]);
  return <pre ref={element} tabIndex={0}>{text || "Waiting for command output…"}</pre>;
}
