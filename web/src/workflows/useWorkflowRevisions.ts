import { useEffect, useMemo, useState } from "react";
import { api, type WorkflowRevision } from "../api";

const emptyRevisions: WorkflowRevision[] = [];

function mergeRevisions(resourceID: string, ...groups: WorkflowRevision[][]) {
  const revisions = new Map<string, WorkflowRevision>();
  for (const group of groups) for (const revision of group) {
    if (revision.resourceId === resourceID) revisions.set(revision.id, revision);
  }
  return [...revisions.values()].sort((left, right) => right.createdAt.localeCompare(left.createdAt) || right.id.localeCompare(left.id));
}

export function useWorkflowRevisions(resourceID: string, overviewRevisions = emptyRevisions) {
  const recent = useMemo(() => mergeRevisions(resourceID, overviewRevisions), [resourceID, overviewRevisions]);
  const [history, setHistory] = useState({ resourceID, revisions: recent, loading: true, error: "" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    setHistory(current => current.resourceID === resourceID
      ? { ...current, revisions: mergeRevisions(resourceID, current.revisions, recent) }
      : { resourceID, revisions: recent, loading: true, error: "" });
  }, [resourceID, recent]);

  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    setHistory(current => ({ ...current, loading: true, error: "" }));
    async function refresh() {
      try {
        const revisions = await api.workflowRevisions(resourceID);
        if (active) setHistory(current => ({ resourceID, revisions: mergeRevisions(resourceID, current.revisions, revisions ?? []), loading: false, error: "" }));
      } catch (cause) {
        if (active) setHistory(current => ({ ...current, loading: false, error: cause instanceof Error ? cause.message : "Run history is unavailable." }));
      } finally {
        if (active) timer = setTimeout(() => void refresh(), 30000);
      }
    }
    void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [resourceID, attempt]);

  const current = history.resourceID === resourceID ? history : { resourceID, revisions: recent, loading: true, error: "" };
  return { revisions: current.revisions, loading: current.loading, error: current.error, retry: () => setAttempt(value => value + 1) };
}
