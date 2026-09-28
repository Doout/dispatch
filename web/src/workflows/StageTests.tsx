import { useEffect, useState } from "react";
import { parseDocument } from "yaml";
import { api, type WorkflowJobResult, type WorkflowResource, type WorkflowRevision, type WorkflowStageRun } from "../api";
import { isPreviewCheckRun } from "./status";

type CheckDefinition = { pipelineRef: string; when: "automatic" | "onDemand" };
type CheckResult = { revision?: WorkflowRevision; jobs?: WorkflowJobResult[]; error?: string };
const terminalStates = new Set(["succeeded", "failed", "cancelled", "superseded", "skipped"]);

// Only show planned tests when the saved definition still describes this run.
export function configuredStageTests(resource: WorkflowResource, revision: WorkflowRevision, name: string): Record<string, CheckDefinition> {
  if (resource.specDigest !== revision.specDigest) return {};
  try {
    const document = parseDocument(resource.document).toJS({ maxAliasCount: 100 });
    const stages = document?.spec?.stages;
    if (!Array.isArray(stages)) return {};
    const checks = stages.find(stage => stage?.name === name)?.checks;
    if (!checks || typeof checks !== "object" || Array.isArray(checks)) return {};
    return Object.fromEntries(Object.entries(checks).flatMap(([key, value]) => {
      const check = value as Partial<CheckDefinition> | null;
      if (!check || typeof check.pipelineRef !== "string") return [];
      if (isPreviewCheckRun(revision) && check.when !== "onDemand") return [];
      return [[key, { pipelineRef: check.pipelineRef, when: check.when === "onDemand" ? "onDemand" : "automatic" }]];
    }));
  } catch {
    return {};
  }
}

export function StageTests({ stage, definitions, command, definitionMatches }: {
  stage: WorkflowStageRun;
  definitions: Record<string, CheckDefinition>;
  command?: string;
  definitionMatches: boolean;
}) {
  const [results, setResults] = useState<Record<string, CheckResult>>({});
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const runs = JSON.stringify(stage.checkRuns ?? {});
  const open = JSON.stringify(expanded);
  const names = [...new Set([...Object.keys(definitions), ...Object.keys(stage.checkRuns ?? {})])].sort((left, right) =>
    Number(definitions[left]?.when === "onDemand") - Number(definitions[right]?.when === "onDemand") || left.localeCompare(right));
  const waitingOnDemand = names.some(name => definitions[name]?.when === "onDemand" && !stage.checkRuns?.[name]);

  useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const entries = Object.entries(JSON.parse(runs) as Record<string, string>);
    const expandedChecks = JSON.parse(open) as Record<string, boolean>;
    async function refresh() {
      const next = await Promise.all(entries.map(async ([name, id]): Promise<[string, CheckResult]> => {
        try {
          const [revision, jobs] = await Promise.all([
            api.workflowRevision(id),
            expandedChecks[name] ? api.workflowJobs(id) : Promise.resolve(undefined),
          ]);
          return [name, { revision, jobs }];
        } catch (cause) {
          return [name, { error: (cause as Error).message }];
        }
      }));
      if (!active) return;
      setResults(current => Object.fromEntries(next.map(([name, result]) => [name, result.error
        ? { ...(current[name]?.revision?.id === stage.checkRuns?.[name] ? current[name] : {}), ...result }
        : result])));
      if (next.some(([, result]) => result.error || !result.revision || !terminalStates.has(result.revision.state))) {
        timer = setTimeout(() => void refresh(), 3000);
      }
    }
    void refresh();
    return () => { active = false; clearTimeout(timer); };
  }, [stage.id, runs, open]);

  return <section className="workflow-stage-tests" aria-label={`${stage.stageName} tests`}>
    <h5>Tests</h5>
    {names.length === 0 && <p>{definitionMatches ? "No health or test checks are configured for this stage." : "No test runs were recorded for this stage."}</p>}
    {names.map(name => {
      const id = stage.checkRuns?.[name];
      const stored = results[name];
      const result: CheckResult | undefined = id && stored?.revision?.id === id ? stored : stored?.error ? { error: stored.error } : undefined;
      const revision = result?.revision;
      const definition = definitions[name];
      const onDemand = definition?.when === "onDemand";
      const state = revision?.state ?? (id ? "loading" : onDemand ? "on_demand" : terminalStates.has(stage.state) ? "not_run" : "pending");
      return <div className="workflow-test-step" key={name}>
        <div className="workflow-test-summary">
          <span className={`status-label ${state}`}><i />{state.replaceAll("_", " ")}</span>
          <div><strong>{name}</strong><small>{definition?.pipelineRef}{definition && ` · ${onDemand ? "On demand" : "Automatic"}`}</small></div>
          {id && <button type="button" className="quiet-button" aria-label={`${expanded[name] ? "Hide" : "View"} ${name} test logs`} aria-expanded={Boolean(expanded[name])} onClick={() => setExpanded(current => ({ ...current, [name]: !current[name] }))}>{expanded[name] ? "Hide logs" : "View logs"}</button>}
        </div>
        {result?.error && <p className="form-error" role="alert">Could not refresh {name}: {result.error}</p>}
        {revision?.error && <p className="form-error" role="alert">{revision.error}</p>}
        {expanded[name] && id && <div className="workflow-test-logs">
          {!result?.jobs && !result?.error && <p>Loading test steps...</p>}
          {result?.jobs?.length === 0 && <p>{revision && terminalStates.has(revision.state) ? "No job output was recorded." : "Waiting for test steps."}</p>}
          {result?.jobs?.map(job => <section className="workflow-job-output" key={job.id} aria-label={`${name} / ${job.jobName} test output`}>
            <header><div><strong>{job.jobName}</strong><span>{job.state}</span></div></header>
            {job.error && <p>{job.error}</p>}
            <pre tabIndex={0}>{job.log || job.error || "Waiting for test output."}</pre>
          </section>)}
        </div>}
      </div>;
    })}
    {waitingOnDemand && <p className="workflow-test-command">{command ? <>Post <code>{command} test</code> on the PR to run on-demand tests against this deployment.</> : "On-demand tests wait for a PR comment. The preview must be deployed before they can start."}</p>}
  </section>;
}
