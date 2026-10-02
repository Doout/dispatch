import { WarningCircle } from "@phosphor-icons/react";
import type { ConfigSource } from "../api";

export function ConfigSourceError({ source }: { source: ConfigSource }) {
  const repository = source.repositoryStatus;
  if (repository && repository.state !== "accessible") return <div className="configuration-sync-error" role="alert"><WarningCircle size={18} weight="fill" aria-hidden="true" /><div><strong>Repository {repository.state.replaceAll("_", " ")} · {source.name}</strong><p>{repository.detail}</p><p className="configuration-sync-hint">{repository.recovery}</p>{source.lastSyncedAt && <p className="configuration-sync-context">Applications keep their last accepted configuration.</p>}</div></div>;
  if (!source.lastError && !["invalid", "degraded"].includes(source.state)) return null;
  const detail = source.lastError?.trim() || "The sync did not return an error detail. Retry the sync to get the current cause.";
  const duplicate = /both define (Application|Pipeline)\//.exec(detail);
  const hint = duplicate
    ? `Give each ${duplicate[1]} a different metadata.name, then sync this configuration again.`
    : /repository access is not configured/i.test(detail)
      ? "Edit this configuration and select a repository credential or GitHub App."
      : /not subscribed to push events/i.test(detail)
        ? "Enable push events for the GitHub App, or switch this configuration to polling."
        : null;
  return <div className="configuration-sync-error" role="alert">
    <WarningCircle size={18} weight="fill" aria-hidden="true" />
    <div>
      <strong>{duplicate ? `Duplicate ${duplicate[1].toLowerCase()} name` : source.state === "degraded" ? (detail.startsWith("Some applications") ? "Some applications need attention" : "Sync warning") : "Configuration sync blocked"} · {source.name}</strong>
      <p>{detail}</p>
      {hint && <p className="configuration-sync-hint">{hint}</p>}
      {duplicate && source.lastSyncedAt && <p className="configuration-sync-context">Affected applications keep their last accepted configuration.</p>}
    </div>
  </div>;
}
