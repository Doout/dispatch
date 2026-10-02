import type { DeploymentHealth as HealthResult } from "./api";

export function DeploymentHealth({ health }: { health?: HealthResult }) {
  if (!health?.state) return null;
  return <details className="deployment-health" open={health.state !== "passed"}>
    <summary>Health checks · {health.state}{health.simulated ? " · simulation" : ""}</summary>
    <p>Captured policy: {health.policy.timeoutSeconds}s timeout, failure threshold {health.policy.failureThreshold}. Workflow tests are reported separately.</p>
    {health.checks.length === 0 ? <p>Checks have not run.</p> : <div className="resource-table-wrap"><table className="resource-table">
      <thead><tr><th>Check</th><th>Requirement</th><th>Result</th><th>Evidence</th></tr></thead>
      <tbody>{health.checks.map(result => <tr key={result.check.id}>
        <td data-label="Check"><strong>{result.check.id}</strong>{result.check.service && <small>{result.check.service}</small>}</td>
        <td data-label="Requirement">{result.check.scope} · {result.check.kind}</td>
        <td data-label="Result">{result.state}</td>
        <td data-label="Evidence">{result.message}{result.httpStatus ? ` HTTP ${result.httpStatus}.` : ""}<small>{result.attempts} attempt(s), {result.failures} consecutive failure(s)</small></td>
      </tr>)}</tbody>
    </table></div>}
  </details>;
}
