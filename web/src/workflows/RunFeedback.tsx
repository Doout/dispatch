import type { Overview, WorkflowFeedback } from "../api";

export function RunFeedback({ feedback, overview }: { feedback: WorkflowFeedback; overview: Overview }) {
  return <section className="workflow-feedback" aria-label="GitHub QA results">
    <h4>GitHub results</h4>
    <p><code>{feedback.statusContext}</code>{!feedback.complete && <span> · Reporting in progress</span>}</p>
    {feedback.targets.map(target => {
      const connection = overview.githubApps.find(app => app.id === target.githubAppId);
      const prURL = target.url || (connection ? `${connection.webUrl.replace(/\/$/, "")}/${target.repository}/pull/${target.number}` : undefined);
      return <div className="workflow-feedback-target" key={`${target.githubAppId}:${target.repository}:${target.number}`}>
        <strong>{prURL ? <a href={prURL} target="_blank" rel="noreferrer">{target.repository} #{target.number}</a> : `${target.repository} #${target.number}`}</strong>
        <code title={target.commitSha}>{target.commitSha.slice(0, 12)}</code>
        <dl><div><dt>Commit status</dt><dd>{target.status || "Waiting to report"}</dd></div><div><dt>PR review</dt><dd>{target.review ? target.review.replaceAll("_", " ") : feedback.reviewOnSuccess === "approve" || feedback.reviewOnFailure === "requestChanges" ? "After QA completes" : "Disabled"}</dd></div></dl>
        {target.skipReason && <p>{target.skipReason}</p>}
        {target.error && <p className="form-error" role="alert">{target.error} Dispatch will retry.</p>}
      </div>;
    })}
  </section>;
}
