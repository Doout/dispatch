import { useEffect, useState } from "react";
import { hostedRequest, type Tenant, type Usage } from "./client";

const metrics = [
  { key: "projects", label: "Projects" },
  { key: "applications", label: "Applications" },
  { key: "members", label: "Members" },
  { key: "builds", label: "Builds" },
  { key: "deployments", label: "Deployments" },
  { key: "buildSeconds", label: "Build time" },
  { key: "runtimeSeconds", label: "Runtime" },
  { key: "storageBytes", label: "Storage" },
  { key: "transferBytes", label: "Transfer" },
];
function formatMetric(key: string, value: number) {
  if (key.endsWith("Seconds"))
    return `${(value / 3600).toLocaleString(undefined, { maximumFractionDigits: 1 })} h`;
  if (key.endsWith("Bytes"))
    return `${(value / 1024 ** 3).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB`;
  return value.toLocaleString();
}
export function UsagePanel({
  tenant,
  onClose,
}: {
  tenant: Tenant;
  onClose: () => void;
}) {
  const [usage, setUsage] = useState<Usage[] | null>(null),
    [error, setError] = useState(""),
    [days, setDays] = useState(30),
    [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let active = true;
    setUsage(null);
    setError("");
    void hostedRequest<Usage[]>(
      `/platform/tenants/${encodeURIComponent(tenant.id)}/usage?days=${days}`,
    )
      .then((value) => {
        if (active) setUsage(value);
      })
      .catch((cause) => {
        if (active) setError((cause as Error).message);
      });
    return () => {
      active = false;
    };
  }, [tenant.id, days, attempt]);
  const latest = usage?.at(-1);
  return (
    <section className="hosted-panel" aria-label={`${tenant.name} usage`}>
      <header className="hosted-panel-header">
        <div>
          <h2>{tenant.name}</h2>
          <p>
            {tenant.slug} · {tenant.state}
          </p>
        </div>
        <button className="quiet-button" onClick={onClose}>
          Close
        </button>
      </header>
      <p>Created {new Date(tenant.createdAt).toLocaleDateString()}</p>
      <label className="hosted-inline-label">
        Usage period
        <select
          value={days}
          onChange={(event) => setDays(Number(event.target.value))}
        >
          <option value={7}>7 days</option>
          <option value={30}>30 days</option>
          <option value={90}>90 days</option>
        </select>
      </label>
      {error && (
        <p className="form-error" role="alert">
          {error}{" "}
          <button
            className="quiet-button"
            onClick={() => setAttempt((v) => v + 1)}
          >
            Retry
          </button>
        </p>
      )}
      {!usage && !error && <p role="status">Loading usage...</p>}
      {usage && (
        <dl className="hosted-usage">
          {metrics.map((metric) => {
            const measured = usage.filter((row) =>
              row.measured.includes(metric.key),
            );
            const value = [
              "projects",
              "applications",
              "members",
              "storageBytes",
            ].includes(metric.key)
              ? latest?.measured.includes(metric.key)
                ? Number(latest[metric.key])
                : undefined
              : measured.length
                ? measured.reduce(
                    (sum, row) => sum + Number(row[metric.key]),
                    0,
                  )
                : undefined;
            return (
              <div key={metric.key}>
                <dt>{metric.label}</dt>
                <dd>
                  {value === undefined
                    ? "Not measured"
                    : formatMetric(metric.key, value)}
                </dd>
              </div>
            );
          })}
        </dl>
      )}
      {usage?.length === 0 && (
        <p>No usage has been recorded for this period.</p>
      )}
    </section>
  );
}
