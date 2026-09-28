import { useEffect, useState, useRef } from "react";
import { ArrowClockwise, Lightning } from "@phosphor-icons/react";
import {
  api,
  type EventActivity,
  type EventActivityPage,
  type EventRule,
} from "./api";
import { PageHeader } from "./PageHeader";
import { StatusLabel } from "./ResourceTable";
import type { EventSection } from "./routes";

const modeName: Record<string, string> = {
  poll: "Polling",
  webhook: "Webhooks",
  webhook_poll: "Polling + webhooks",
};
const kindName: Record<string, string> = {
  configuration: "Repository configuration",
  template: "Preview template",
  preview: "PR preview",
  trigger: "Application",
  group: "Preview group",
};
const transportName: Record<string, string> = {
  poll: "Polling",
  webhook: "Webhook",
  history: "Earlier activity",
};
function timeLabel(value: string) {
  return new Date(value).toLocaleString();
}
function checkLabel(rule: EventRule) {
  if (!rule.enabled) return "Paused";
  if (rule.mode === "webhook") return "Waiting for webhooks";
  if (!rule.check) return "Waiting for first check";
  if (rule.check.state === "failed") return "Check failed";
  const seconds =
    rule.intervalSeconds && rule.intervalSeconds >= 30
      ? rule.intervalSeconds
      : 60;
  return Date.now() - new Date(rule.check.createdAt).getTime() >
    Math.max(90, seconds * 2) * 1000
    ? "Check overdue"
    : "Polling healthy";
}

export function EventsListPage({
  section,
  onSectionChange,
  onConfigure,
  canConfigure,
  onEditHooks,
  onOpenRun,
  accessVersion = "",
}: {
  accessVersion?: string;
  section: EventSection;
  onSectionChange: (section: EventSection) => void;
  onConfigure: () => void;
  canConfigure: boolean;
  onOpenRun: (id: string) => Promise<void>;
  onEditHooks: (rule: EventRule) => void;
}) {
  const [rules, setRules] = useState<EventRule[]>();
  const [page, setPage] = useState<EventActivityPage>();
  const [ruleError, setRuleError] = useState("");
  const [activityError, setActivityError] = useState("");
  const [transport, setTransport] = useState("");
  const [retry, setRetry] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const feedVersion = useRef(0);
  useEffect(() => {
    feedVersion.current++;
    setLoadingMore(false);
    setRules(undefined);
    let alive = true,
      pending = false;
    setPage(undefined);
    setActivityError("");
    async function refresh() {
      if (pending) return;
      pending = true;
      const [ruleResult, activityResult] = await Promise.allSettled([
        api.eventRules(),
        api.eventActivity(transport),
      ]);
      if (alive) {
        if (ruleResult.status === "fulfilled") {
          setRules(ruleResult.value);
          setRuleError("");
        } else setRuleError((ruleResult.reason as Error).message);
        if (activityResult.status === "fulfilled") {
          setPage((old) =>
            old && old.items.length > 50
              ? {
                  items: [
                    ...activityResult.value.items,
                    ...old.items.filter(
                      (item) =>
                        !activityResult.value.items.some(
                          (next) => next.id === item.id,
                        ),
                    ),
                  ],
                  next: old.next,
                  total: activityResult.value.total,
                }
              : activityResult.value,
          );
          setActivityError("");
        } else setActivityError((activityResult.reason as Error).message);
      }
      pending = false;
    }
    void refresh();
    const timer = window.setInterval(() => {
      void refresh();
    }, 30_000);
    return () => {
      feedVersion.current++;
      alive = false;
      window.clearInterval(timer);
    };
  }, [transport, retry, accessVersion]);
  async function more() {
    if (!page?.next || loadingMore) return;
    const generation = feedVersion.current;
    setLoadingMore(true);
    setActivityError("");
    try {
      const result = await api.eventActivity(transport, page.next);
      if (generation !== feedVersion.current) return;
      setPage((old) =>
        old
          ? {
              ...result,
              items: [
                ...old.items,
                ...result.items.filter(
                  (item) =>
                    !old.items.some((previous) => previous.id === item.id),
                ),
              ],
            }
          : result,
      );
    } catch (cause) {
      if (generation === feedVersion.current)
        setActivityError((cause as Error).message);
    } finally {
      if (generation === feedVersion.current) setLoadingMore(false);
    }
  }
  return (
    <div className="page-layout events-page">
      <PageHeader
        view="events"
        action={
          canConfigure
            ? { label: "Configure in applications", onClick: onConfigure }
            : undefined
        }
      />
      <div
        className="application-sections event-tabs"
        role="tablist"
        aria-label="Events"
      >
        <button
          role="tab"
          aria-selected={section === "rules"}
          className={section === "rules" ? "active" : ""}
          onClick={() => onSectionChange("rules")}
        >
          Rules {rules && !ruleError && <span>{rules.length}</span>}
        </button>
        <button
          role="tab"
          aria-selected={section === "activity"}
          className={section === "activity" ? "active" : ""}
          onClick={() => onSectionChange("activity")}
        >
          Activity {page && !activityError && <span>{page.total}</span>}
        </button>
      </div>
      <section
        className="event-section"
        aria-label={section === "rules" ? "Event rules" : "Event activity"}
      >
        <div className="section-toolbar">
          <h2>{section === "rules" ? "Event rules" : "Event activity"}</h2>
          <div className="events-toolbar-actions">
            {section === "activity" && (
              <label>
                Delivery{" "}
                <select
                  value={transport}
                  onChange={(event) => setTransport(event.target.value)}
                >
                  <option value="">All methods</option>
                  <option value="poll">Polling</option>
                  <option value="webhook">Webhooks</option>
                  <option value="history">Earlier activity</option>
                </select>
              </label>
            )}
            <button
              className="quiet-button"
              onClick={() => setRetry((value) => value + 1)}
            >
              <ArrowClockwise size={16} /> Refresh
            </button>
          </div>
        </div>
        {section === "rules" ? (
          <>
            {ruleError && (
              <p className="event-error" role="alert">
                Could not load event rules: {ruleError}
              </p>
            )}
            {!rules && !ruleError && (
              <p role="status">Loading event rules...</p>
            )}
            {rules && !!rules.length && (
              <div className="resource-table-wrap">
                <table className="resource-table event-table">
                  <thead>
                    <tr>
                      <th>Rule</th>
                      <th>Watched sources</th>
                      <th>Delivery</th>
                      <th>Last check</th>
                      <th>
                        <span className="sr-only">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {rules.map((rule) => (
                      <tr key={rule.id}>
                        <td data-label="Rule">
                          <strong>{rule.name}</strong>
                          <small>
                            {kindName[rule.kind]}
                            {rule.command && (
                              <>
                                {" "}
                                · <code>{rule.command}</code>
                              </>
                            )}
                          </small>
                          {rule.error && rule.error !== rule.check?.message && (
                            <small className="event-error">{rule.error}</small>
                          )}
                        </td>
                        <td data-label="Watched sources">
                          <div className="event-sources">
                            {rule.repositories.map((repo) => (
                              <small key={repo}>
                                {repo}
                                {rule.pullRequest
                                  ? ` #${rule.pullRequest}`
                                  : ""}
                              </small>
                            ))}
                            {rule.branch && <code>{rule.branch}</code>}
                          </div>
                        </td>
                        <td data-label="Delivery">
                          {modeName[rule.mode] ?? rule.mode}
                          {rule.mode !== "webhook" && (
                            <small>
                              Every{" "}
                              {rule.intervalSeconds &&
                              rule.intervalSeconds >= 30
                                ? rule.intervalSeconds
                                : 60}{" "}
                              seconds
                            </small>
                          )}
                        </td>
                        <td data-label="Last check">
                          <strong
                            className={
                              rule.check?.state === "failed"
                                ? "event-error"
                                : ""
                            }
                          >
                            {checkLabel(rule)}
                          </strong>
                          {rule.check && (
                            <>
                              <small>
                                {rule.check.repository} ·{" "}
                                {timeLabel(rule.check.createdAt)}
                              </small>
                              {rule.check.state === "failed" && (
                                <small className="event-error">
                                  {rule.check.message}
                                </small>
                              )}
                            </>
                          )}
                        </td>
                        <td className="row-actions">
                          {canConfigure && (
                            <a className="table-action" href="/applications">
                              Configure
                            </a>
                          )}
                          {rule.canEditHooks &&
                            (rule.kind === "group" ||
                              rule.kind === "trigger") && (
                              <button
                                className="table-action"
                                onClick={() => onEditHooks(rule)}
                              >
                                <Lightning size={16} /> Hooks
                              </button>
                            )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {rules?.length === 0 && !ruleError && (
              <div className="events-empty">
                <h3>No event rules</h3>
                <p>
                  Add repository polling or a PR preview template in
                  Applications.
                </p>
              </div>
            )}
          </>
        ) : (
          <>
            {activityError && (
              <p className="event-error" role="alert">
                Could not load event activity: {activityError}
              </p>
            )}
            {!page && !activityError && (
              <p role="status">Loading event activity...</p>
            )}
            {page && !!page.items.length && (
              <div className="resource-table-wrap">
                <table className="resource-table event-table">
                  <thead>
                    <tr>
                      <th>Event</th>
                      <th>Source</th>
                      <th>Outcome</th>
                      <th>Received</th>
                      <th>Run</th>
                    </tr>
                  </thead>
                  <tbody>
                    {page.items.map((item) => (
                      <ActivityRow
                        key={item.id}
                        item={item}
                        onOpenRun={onOpenRun}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
            {page?.items.length === 0 && !activityError && (
              <div className="events-empty">
                <h3>
                  No{" "}
                  {transport
                    ? transportName[transport].toLowerCase() + " "
                    : ""}
                  activity yet
                </h3>
                <p>
                  New commits, preview commands, and PR closures appear here
                  when Dispatch processes them. Routine checks are shown on the
                  Rules tab.
                </p>
              </div>
            )}
            {page?.next && (
              <button
                className="quiet-button"
                disabled={loadingMore}
                onClick={() => {
                  void more();
                }}
              >
                {loadingMore ? "Loading..." : "Load more"}
              </button>
            )}
          </>
        )}
      </section>
    </div>
  );
}
function ActivityRow({
  item,
  onOpenRun,
}: {
  item: EventActivity;
  onOpenRun: (id: string) => Promise<void>;
}) {
  const [error, setError] = useState("");
  const [opening, setOpening] = useState(false);
  async function open(id: string) {
    setOpening(true);
    setError("");
    try {
      await onOpenRun(id);
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setOpening(false);
    }
  }
  return (
    <tr>
      <td data-label="Event">
        <strong>{item.name}</strong>
        <small>{item.kind.replaceAll("_", " ")}</small>
      </td>
      <td data-label="Source">
        <span>
          {item.repository}
          {item.pullRequest ? ` #${item.pullRequest}` : ""}
        </span>
        <small>
          {transportName[item.transport] ?? item.transport}
          {item.branch ? ` · ${item.branch}` : ""}
        </small>
        {item.commitSha && <code>{item.commitSha.slice(0, 12)}</code>}
      </td>
      <td data-label="Outcome">
        <StatusLabel state={item.state} />
        {item.message && <small className="event-error">{item.message}</small>}
      </td>
      <td data-label="Received">{timeLabel(item.createdAt)}</td>
      <td data-label="Run">
        {error && (
          <small role="alert" className="event-error">
            {error}
          </small>
        )}
        <div className="event-sources">
          {item.revisionIds?.map((id) => (
            <button
              className="table-action"
              key={id}
              disabled={opening}
              onClick={() => {
                void open(id);
              }}
            >
              View run {id.slice(-8)}
            </button>
          ))}
          {item.resourceId && (
            <a
              href={`/applications/${encodeURIComponent(item.resourceId)}/topology`}
            >
              Workflow
            </a>
          )}
          {item.previewUrl && (
            <a href={item.previewUrl} target="_blank" rel="noreferrer">
              Open preview
            </a>
          )}
        </div>
      </td>
    </tr>
  );
}
