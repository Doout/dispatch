import { WorkflowResourceDialog as EventWorkflowResourceDialog } from "../workflows/ResourceDialog";
import { EventsListPage } from "../EventsPage";
import { FormEvent, useEffect, useState } from "react";
import { ArrowLeft } from "@phosphor-icons/react";
import {
  api,
  Deployment,
  EventTrigger,
  Overview,
  PreviewGroup,
  PreviewGroupComponent,
  Secret,
} from "../api";
import { EventSection } from "../routes";
import { HookCredentialBindings, HookFields } from "../HookEditorFields";
import { PageHeader } from "../PageHeader";
import { canManageAnyProject } from "../permissions";

type EventHookTarget =
  | { type: "trigger"; trigger: EventTrigger }
  | { type: "group"; group: PreviewGroup };

export function EventsPage({
  overview,
  section,
  onSectionChange,
  onConfigure,
  onChanged,
}: {
  overview: Overview;
  section: EventSection;
  onSectionChange: (section: EventSection) => void;
  onConfigure: () => void;
  onChanged: () => Promise<void>;
}) {
  const [hookTarget, setHookTarget] = useState<EventHookTarget | null>(null);
  const [eventRun, setEventRun] = useState<import("../api").WorkflowRevision>();
  const [runLinkError, setRunLinkError] = useState("");
  useEffect(() => {
    const id = new URLSearchParams(window.location.search).get("run");
    if (!id) return;
    let active = true;
    void api.workflowRevision(id).then(revision => {
      if (active) setEventRun(revision);
    }).catch(cause => { if (active) setRunLinkError(`Could not open the QA run: ${(cause as Error).message}`); });
    return () => { active = false; };
  }, []);

  if (hookTarget)
    return (
      <div className="page-layout editor-page">
        <PageHeader
          view="events"
          action={{
            label: "Back to events",
            onClick: () => setHookTarget(null),
            icon: <ArrowLeft size={16} />,
            tone: "quiet",
          }}
        />
        <EventHookEditor
          key={
            hookTarget.type === "trigger"
              ? hookTarget.trigger.id
              : hookTarget.group.id
          }
          target={hookTarget}
          secrets={overview.secrets}
          onClose={() => setHookTarget(null)}
          onSaved={async () => {
            setHookTarget(null);
            await onChanged();
          }}
        />
      </div>
    );
  const eventResource = overview.workflowResources?.find(item => item.id === eventRun?.resourceId);
  return <>
    {runLinkError && <p className="form-error" role="alert">{runLinkError}</p>}
    <EventsListPage
      accessVersion={JSON.stringify([overview.identity, overview.projectPermissions])}
      section={section}
      onSectionChange={onSectionChange}
      onConfigure={onConfigure}
      canConfigure={canManageAnyProject(overview, "project.configure")}
      onOpenRun={async id => {
        const revision = await api.workflowRevision(id);
        if (!overview.workflowResources?.some(item => item.id === revision.resourceId)) {
          throw new Error("This workflow is no longer available.");
        }
        setEventRun(revision);
      }}
      onEditHooks={rule => {
        if (rule.kind === "group") {
          const group = overview.previewGroups.find(item => `group:${item.id}` === rule.id);
          if (group) setHookTarget({ type: "group", group });
        }
        if (rule.kind === "trigger") {
          const trigger = overview.eventTriggers.find(item => `trigger:${item.id}` === rule.id);
          if (trigger) setHookTarget({ type: "trigger", trigger });
        }
      }}
    />
    {eventRun && eventResource && <EventWorkflowResourceDialog
      key={eventRun.id}
      resource={eventResource}
      initialRevisionID={eventRun.id}
      overview={{ ...overview, workflowRevisions: [eventRun, ...(overview.workflowRevisions ?? []).filter(item => item.id !== eventRun.id)] }}
      onClose={() => setEventRun(undefined)}
      onChanged={onChanged}
      onOpenDeploymentManifests={id => { window.location.href = `/deployments/${encodeURIComponent(id)}/manifests`; }}
    />}
  </>;
}

function EventHookEditor({
  target,
  secrets,
  onClose,
  onSaved,
}: {
  target: EventHookTarget;
  secrets: Secret[];
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const trigger = target.type === "trigger" ? target.trigger : undefined;
  const group = target.type === "group" ? target.group : undefined;
  const [preDeployHook, setPreDeployHook] = useState(
    trigger?.preDeployHook ?? "",
  );
  const [postDeployHook, setPostDeployHook] = useState(
    trigger?.postDeployHook ?? "",
  );
  const [secretIds, setSecretIds] = useState<string[]>(
    trigger?.secretIds ?? [],
  );
  const [components, setComponents] = useState<PreviewGroupComponent[]>(
    group?.components.map((component) => ({ ...component })) ?? [],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  function changeComponent(
    index: number,
    values: Partial<PreviewGroupComponent>,
  ) {
    setComponents((current) =>
      current.map((component, componentIndex) =>
        componentIndex === index ? { ...component, ...values } : component,
      ),
    );
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      if (trigger) {
        await api.updateEventTrigger(trigger.id, {
          command: trigger.command,
          enabled: trigger.enabled,
          preDeployHook,
          postDeployHook,
          secretIds,
        });
      } else if (group) {
        await api.updatePreviewGroup(group.id, {
          name: group.name,
          githubAppId: group.githubAppId ?? "",
          command: group.command,
          enabled: group.enabled,
          components,
        });
      }
      await onSaved();
    } catch (cause) {
      setError((cause as Error).message);
    } finally {
      setBusy(false);
    }
  }

  const title = trigger ? overviewRuleName(trigger) : group!.name;
  return (
    <section
      className="event-hook-editor"
      aria-labelledby="event-hook-editor-title"
    >
      <header>
        <div>
          <h2 id="event-hook-editor-title">Deployment hooks: {title}</h2>
        </div>
      </header>
      <form onSubmit={save} aria-busy={busy}>
        {trigger ? (
          <HookFields
            preDeployHook={preDeployHook}
            postDeployHook={postDeployHook}
            onPreDeployHook={setPreDeployHook}
            onPostDeployHook={setPostDeployHook}
          />
        ) : (
          components.map((component, index) => (
            <fieldset
              className="event-component-hooks"
              key={component.id ?? component.alias}
            >
              <legend>
                {component.alias || `Component ${index + 1}`}{" "}
                <span>{component.repository}</span>
              </legend>
              <HookFields
                preDeployHook={component.preDeployHook ?? ""}
                postDeployHook={component.postDeployHook ?? ""}
                onPreDeployHook={(value) =>
                  changeComponent(index, { preDeployHook: value })
                }
                onPostDeployHook={(value) =>
                  changeComponent(index, { postDeployHook: value })
                }
              />
              <HookCredentialBindings
                secrets={secrets}
                selected={component.secretIds ?? []}
                onChange={(secretIds) => changeComponent(index, { secretIds })}
              />
            </fieldset>
          ))
        )}
        {trigger && (
          <HookCredentialBindings
            secrets={secrets}
            selected={secretIds}
            onChange={setSecretIds}
          />
        )}
        <details className="event-hook-context">
          <summary>Event variables (8)</summary>
          <div>
            {[
              "DISPATCH_PREVIEW_TAG",
              "DISPATCH_EVENT_REPOSITORY",
              "DISPATCH_EVENT_PULL_REQUEST_NUMBER",
              "DISPATCH_EVENT_HEAD_REF",
              "DISPATCH_EVENT_HEAD_SHA",
              "DISPATCH_EVENT_ACTOR",
              "DISPATCH_EVENT_COMMAND",
              "DISPATCH_EVENT_ARGUMENTS",
            ].map((name) => (
              <code key={name}>{name}</code>
            ))}
          </div>
        </details>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <div className="builder-actions">
          <button type="button" className="quiet-button" onClick={onClose}>
            Cancel
          </button>
          <button className="primary-button" disabled={busy}>
            {busy ? "Saving..." : "Save hooks"}
          </button>
        </div>
      </form>
    </section>
  );
}

function overviewRuleName(trigger: EventTrigger) {
  return `${trigger.repository} ${trigger.command}`;
}
