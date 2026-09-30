import type { WorkflowJobResult, WorkflowRevision, WorkflowStageRun } from "../api";

export type BuildStepTiming = { label: string; durationMs: number };

function timestamp(value?: string): number | undefined {
  if (!value) return undefined;
  const time = Date.parse(value);
  return Number.isFinite(time) && time >= Date.UTC(2000, 0, 1) ? time : undefined;
}

function duration(start?: string, finish?: string, now = Date.now(), running = false): number | undefined {
  const from = timestamp(start);
  const to = timestamp(finish) ?? (running ? now : undefined);
  return from === undefined || to === undefined || to < from ? undefined : to - from;
}

function wallTime(items: Array<{ startedAt?: string; finishedAt?: string; state: string }>, now: number): number | undefined {
  const starts = items.map((item) => timestamp(item.startedAt)).filter((value): value is number => value !== undefined);
  if (starts.length === 0) return undefined;
  const ends = items.map((item) => timestamp(item.finishedAt) ?? (["running", "awaiting_approval"].includes(item.state) ? now : undefined)).filter((value): value is number => value !== undefined);
  return ends.length === 0 ? undefined : Math.max(0, Math.max(...ends) - Math.min(...starts));
}

export function itemDuration(item: { startedAt?: string; finishedAt?: string; state: string }, now = Date.now()): number | undefined {
  return duration(item.startedAt, item.finishedAt, now, ["running", "awaiting_approval"].includes(item.state));
}

export function buildLogTiming(log?: string): { cachedSteps: number; slowest: BuildStepTiming[] } {
  const labels = new Map<string, string>();
  const durations = new Map<string, number>();
  const cached = new Set<string>();
  for (const raw of (log ?? "").split(/\r?\n/)) {
    const line = raw.replace(/\x1b\[[0-9;]*[A-Za-z]/g, "");
    const label = /^#(\d+) \[([^\]]+)\] (.+)$/.exec(line);
    if (label) labels.set(label[1], `${label[2]} · ${label[3]}`);
    const finished = /^#(\d+) DONE ([\d.]+)s\s*$/.exec(line);
    if (finished) durations.set(finished[1], Number(finished[2]) * 1000);
    const hit = /^#(\d+) CACHED\s*$/.exec(line);
    if (hit) cached.add(hit[1]);
  }
  return {
    cachedSteps: cached.size,
    slowest: [...durations].filter(([id, ms]) => labels.has(id) && Number.isFinite(ms) && ms > 0)
      .map(([id, durationMs]) => ({ label: labels.get(id)!, durationMs }))
      .sort((left, right) => right.durationMs - left.durationMs).slice(0, 3),
  };
}

export function runTiming(revision: WorkflowRevision, jobs: WorkflowJobResult[], stages: WorkflowStageRun[], now = Date.now()) {
  const running = ["queued", "running", "awaiting_approval"].includes(revision.state);
  const total = duration(revision.createdAt, revision.finishedAt, now, running);
  const queue = duration(revision.createdAt, revision.startedAt);
  const builds = wallTime(jobs, now);
  const stageTime = wallTime(stages, now);
  const measured = (queue ?? 0) + (builds ?? 0) + (stageTime ?? 0);
  return {
    total,
    queue,
    builds,
    stages: stageTime,
    other: total === undefined ? undefined : Math.max(0, total - measured),
    reusedJobs: jobs.filter((job) => Boolean(job.reusedFromId)).length,
    cachedSteps: jobs.reduce((count, job) => count + buildLogTiming(job.log).cachedSteps, 0),
  };
}

export function formatRunDuration(ms?: number): string {
  if (ms === undefined) return "—";
  if (ms > 0 && ms < 1000) return "<1s";
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remaining = seconds % 60;
  return remaining ? `${minutes}m ${remaining}s` : `${minutes}m`;
}
