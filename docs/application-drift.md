# Application sync and drift

Open **Sync and drift** from a Helm application's menu. Deployment details show a compact **Application status** strip with icons and labels; expand **Details** for timestamps, revision IDs, resource differences, and reapply controls. This strip always describes the current application, including when viewing an older deployment.
The four indicators describe different things:

| Indicator | Meaning |
| --- | --- |
| Configuration sync | The last repository configuration accepted by Dispatch, including paused or invalid sources. Directly configured applications show Not applicable. |
| Deployment revision | The successful deployed revision compared with the configuration and workflow revisions already observed by Dispatch. It does not fetch Git when opening the page. |
| Runtime drift | Whether live Kubernetes resources match the fields saved with the last successful deployment. |
| Resource health | Readiness observed during the check, independent of configuration differences. |

## Checks

Project admins and operators can select **Check now**. Checks run from the Dispatch
controller with a 30-second deadline. Viewers and deployers can inspect saved
results. Each observation records its time, location, and the last successful
check time. An unreachable target, denied resource read, or ownership conflict
produces **Unknown**. A failed check replaces the previous status; its older
successful-check timestamp remains visible.

Checks are manual. There is no scheduled monitoring, Argo CD integration, or
automatic repair. A saved green observation does not establish continuous health.

## Saved configuration

With vault storage configured, successful Helm deployments on Kubernetes and
OpenShift save an encrypted copy of their rendered resources. This includes
Dispatch-owned service binding Secrets. The copy is separate from public deployment
snapshots, and is bound to the deployment ID by authenticated encryption.

If baseline capture fails, the deployment remains successful and its log records a
sanitized warning. Drift stays Unknown and reapply is unavailable for that deployment.

Existing deployments without this baseline show Unknown. Deploy once after
upgrading to enable checks for that application. Docker/Compose runtimes and
workflow jobs/hooks are outside this feature. Helm hook resources are excluded.
Charts must emit individually named resources rather than Kubernetes List objects.
Checks support up to 500 resources per deployment.

Only fields present in the saved manifest are compared. Runtime status, managed
fields, resource versions, generated timestamps, and last-applied annotations are
ignored. Named container/environment entries, ports, and mount paths are matched
by identity. Server-added fields and injected named entries remain untouched.
Equivalent Kubernetes CPU and memory quantities compare equal.

Explicitly configured replica counts are compared, including when an autoscaler
changes them. Omit replicas from the chart manifest when an autoscaler should own
that field. Dispatch does not prune resources absent from the saved manifest.

Images and ordinary numeric/boolean differences are readable. Potentially
sensitive fields and other string values are masked, including ConfigMap values,
environment literals, and Secret data. The UI shows at most 200 differences per
resource and labels truncated results.

Deployment, StatefulSet, DaemonSet, Pod, Job, and PVC readiness is evaluated.
Configuration-only resources show Not applicable; resource kinds without a known
readiness rule show Unknown. Missing resources are degraded. This does not test
application endpoints or external dependencies.

## Reapply deployed configuration

Users with deployment permission can explicitly reapply the current successful
baseline. The UI requires confirmation before restoring the last successful deployment. A newer
successful deployment invalidates a stale reapply request.

Reapply restores saved fields and recreates missing resources. It preserves saved
service credentials, regardless of later service edits or external rotation. It
does not fetch Git, render a new chart, run hooks, or create a new Helm revision.
A regular deployment is required to adopt new code or credentials.

All resource changes are first submitted as Kubernetes dry runs. Permission,
validation, or immutable-field failures stop the operation before writes begin.
Existing-resource patches use resource-version preconditions, and resource
ownership is checked. A resource claimed by another Helm release is not replaced.
The application cannot deploy or clean up concurrently with its check or repair.

Kubernetes does not provide a transaction across resources. A conflict or outage
between preflight and writes can leave a partial repair. Dispatch stops, records
the failure, and checks the resulting state. Reapply history records the actor,
deployment, time, and outcome. Saved Helm release history is not modified.

## API

- `GET /api/v1/apps/{id}/sync`: saved observation, configuration/revision status, and recent reapply history. Requires project view permission.
- `POST /api/v1/apps/{id}/drift/check`: perform and persist a check. Requires project configuration permission.
- `POST /api/v1/apps/{id}/reapply`: restore the selected successful baseline. Requires deployment permission and a JSON body such as `{"deploymentId":"01..."}`.

Project boundaries apply to all endpoints. Unsupported applications return a
readable status; mutation requests are rejected. A busy application or stale
reapply request returns HTTP 409. An observed target failure returns HTTP 200 with
Unknown status so clients can display the timestamp and failure observation.

## Integration tests

Use disposable infrastructure only:

```sh
DISPATCH_DRIFT_KUBECONFIG=/path/to/disposable/kubeconfig \
  go test -race ./internal/drift -run TestHelmDriftReapplyIntegration -v
DISPATCH_DRIFT_POSTGRES_URL='postgres://user:password@localhost/drift_test?sslmode=disable' \
  go test -race ./internal/drift -run TestDriftPostgresPersistenceIntegration -v
```

The cluster test installs a unique Helm release, edits its image, replicas and
configuration, deletes resources, reapplies the encrypted baseline, and verifies
credential preservation and ownership protection. It deletes its namespace on
completion. The PostgreSQL test requires an empty database.

## Deployment history and comparisons

Open **History** in deployment details to browse this application's saved runs.
History loads 50 runs at a time and includes successful, failed, cancelled, and
in-progress deployments. **Load older deployments** retrieves the next page.

Choose **From** and **To** to compare saved inputs, including supplied Helm values,
release settings, code revisions, and applied service configuration revisions.
Added, removed, and changed fields are listed with before/after values and a path
filter. Comparisons use snapshots, never the application's current editable
settings or the cluster's current release. A failed deployment's snapshot describes
its attempted inputs, not a successful rollout. Chart defaults and live resource
changes are outside this comparison.

Sensitive fields are excluded. Their presence does not establish whether a hidden
value changed. Older records without saved inputs show **Comparison unavailable**.
The response includes at most 1,000 changed fields and identifies truncation.

- `GET /api/v1/apps/{id}/deployment-history?before={deploymentId}`: paginated saved runs for an application. The response contains `items` and an optional `next` cursor.
- `GET /api/v1/deployments/{id}/compare?from={deploymentId}`: compare two deployments belonging to the same application. Requires project view permission, as does history.
