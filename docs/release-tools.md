# Release tools

Deployment details include a compact Release tools panel with release notes,
source links, activity, failure diagnosis, a deployment preview, and historical
rollback. Each tool updates in place. Historical navigation retains the selected
application and does not reload the document.

## Preview a deployment

The deployment form reviews the selected revision before starting it. Preview
resolves configured source credentials and service fields without returning their
values. Helm previews render the chart, validate chart values, and submit
Kubernetes server-side dry-run requests, including the planned service Secrets.
No containers, releases, Secrets, or other runtime resources are changed.

Checks distinguish `passed`, `failed`, and `unavailable`. Hooks and image builds
run only during deployment because they can have side effects. A namespace that
does not yet exist is an explicit admission-validation gap; deployment creates
that namespace. Admission validation may also require CRDs to be installed first.
Preview does not guarantee rollout success. Reviewed deployment requests include
the application name used to select the release, project, application
configuration digest, binding mapping digest, and service
revisions. Dispatch checks those inputs in the same transaction that accepts the
deployment and captures its bindings. Changed local inputs require another review.
Git sources resolve to a fixed commit. External credentials and cluster state can
still change after the observation.

Expected changes compare sanitized saved inputs against the last successful
deployment. Sensitive values, chart defaults, and live edits are excluded.
Repository-managed applications use their environment approval/promotion flow.

## Restore a retained release

Select a successful deployment in History, open Release tools, and review rollback
to that version. Dispatch requires one retained successful Helm revision whose
target, release, saved values, and deployment time match the selected run. It
checks that the original chart and immutable service Secrets still exist, validates
the desired resources using server-side dry-run, and asks the operator to confirm
the selected version and current running release.

The accepted rollback is a new deployment with its own actor audit record. Its
snapshot and captured binding records copy the selected deployment in one
transaction. Native Helm rollback restores the retained chart and values using
the original Secret references. Current application settings and newly rotated
credentials do not replace those inputs. Retained newer Secrets remain available
to their releases.

Rollback does not run Dispatch hooks or Helm hooks. It does not reverse database
migrations or external side effects. It cannot restore an application after its
target, namespace, or release name changes. Missing or ambiguous history and
missing credentials produce an explicit unavailable reason.

Docker and Compose deployments retain resolved runtime inputs in encrypted storage
when the controller has a master key configured. Review shows the target, exact
image IDs, container port and configured domain. The images must still exist on
the original Docker target. Restore uses those images and the original
resolved environment, commands, mounts and service networks. It never fetches
source, builds images or runs hooks. Dispatch records the restore as a new
deployment and preserves the selected successful deployment. Enrolled targets
keep those artifacts in the agent's private state directory and use durable
[remote runtime receipts](remote-runtime.md) for restore.

Compose retains referenced files from the source checkout, including configs and
secrets, up to 32 MiB per deployment. Source mounts must be read-only and cannot
contain symlinks. Writable data belongs in named volumes or persistent host paths.
Shared host paths must still exist at restore time. Cleanup removes owned
containers and networks and leaves volume and external database data intact.
Keep the master key and artifact storage with the controller's backups. Image
pruning can make a historical revision unavailable.

The review identifies the current deployment, application settings, target and
live runtime. A changed review fails before accepting the restore; execution
checks again before applying it. External tools can still change Docker or Helm
resources during an operation. A failed or cancelled restore records the result
and requires another review before retrying. Inspect runtime readiness first.
With [managed Docker routing](application-routing.md), the restored candidate
must pass its retained health policy before the route switches. Database changes
remain outside release rollback.

## Diagnose and inspect activity

Diagnosis reads current pods, recent events, and bounded log excerpts from the
Dispatch controller. Image-pull failures, scheduling failures, crash loops, memory
termination, and readiness failures include concrete next steps. Historical
deployment selection is labeled separately from current workload inspection.

Inspection requires an idle application so that a concurrent deployment cannot
replace the runtime inputs during credential redaction. Dispatch redacts captured
credentials from the selected run, current successful release, and latest attempt.
If historical external credentials cannot be resolved safely, log and event
details remain hidden. Diagnosis never treats unreadable resources as healthy.

The activity timeline combines retained deployments, workflow revisions/builds/
stages, observations, reapply actions, release actions, and authenticated mutation
audit metadata. Release notes store operator context separately from execution
inputs. Source links require HTTPS and cannot contain embedded credentials.
Do not put credentials into release notes.

## Approvals and recovery

The approval workspace shows the exact workflow revision and source commits.
Concurrent approval requests are serialized. A paused or invalid application, or
configuration that changed since the captured revision, cannot be approved under
the old review. Start a new revision after changing configuration.

Before accepting traffic, the controller marks interrupted queued/running workflow
revisions, jobs, and stages as failed with explicit inspection and retry guidance.
Pending approvals and successful build outputs remain intact. Recovery never
automatically repeats a job or a deployment whose external effects are uncertain.
This startup reconciliation assumes a single Dispatch controller.

## API

| Endpoint | Permission and behavior |
| --- | --- |
| `POST /api/v1/apps/{id}/release-preview` | `deployment.run`; optional `revision`, returns sanitized checks and saved-input changes. |
| `GET /api/v1/apps/{id}/activity` | `project.view`; up to 200 recent retained timeline events. |
| `GET /api/v1/deployments/{id}/release` | `project.view`; notes and source links. |
| `PUT /api/v1/deployments/{id}/release` | `project.configure`; `notes` and HTTPS `links`, with an actor audit. |
| `POST /api/v1/deployments/{id}/rollback-preview` | `deployment.run`; retained artifact/credential validation and current deployment identity. |
| `POST /api/v1/deployments/{id}/rollback` | `deployment.run`; requires `confirmDeploymentId`, `expectedCurrentDeploymentId`, `expectedReviewDigest`, and `confirmDatabaseNotReverted: true`. |
| `GET /api/v1/deployments/{id}/diagnosis` | `project.view`; bounded current workload observation and sanitized log excerpts. |

`POST /api/v1/apps/{id}/deployments` accepts the optional `review` object returned
by preview alongside `commitSha`. An incomplete review returns 422; changed
reviewed inputs return 409 and do not create a deployment. Existing clients can
continue to create an ordinary deployment without the review object.

SQLite and PostgreSQL migration `042_release_tools` stores notes and release
actions. Existing applications require no data conversion. The runtime integration
test uses a disposable cluster through `DISPATCH_RELEASE_KUBECONFIG`; persistence
also supports `DISPATCH_RELEASE_POSTGRES_URL` pointing to a disposable database.

## Confirm destructive actions

The UI asks for the exact resource name before deleting a registration or cleaning
up deployed resources. It shows the resource identity, target and data consequences.
The API enforces the same confirmation for direct clients.

For a DELETE endpoint, first POST to that endpoint's `/delete-preview`. For cleanup,
credential revocation and credential rotation, POST to the action's `-preview`
endpoint. The response includes `resourceId`, `action`, `name` and `version`.
Send this object alongside the original action fields:

```json
{"confirmation":{"resourceId":"application-id","action":"delete","confirmName":"Orders","expectedVersion":"version-from-review"}}
```

The original endpoint permissions still apply. Missing or incorrect confirmation
returns 422. Changed resource state returns 409 and requires a fresh review.
Cancellation sends no destructive request. The audit retains the confirmed name,
resource, version, actor and request outcome even after the resource is deleted.
Existing API clients that delete resources must adopt this review flow.

Cleaning up or deleting a generated preview application removes the entire
preview, including applications retained through older workflow stages, and closes
its PR triggers. The review lists that full scope and preserves application
configuration and deployment history. Changes to a listed application's inputs or
the preview invalidate the review before cleanup starts.

Migration `067_runtime_artifacts` adds encrypted runtime inputs and confirmed audit
metadata for SQLite and PostgreSQL. Run the local Docker/Compose restore tests
with `DISPATCH_ROLLBACK_DOCKER_INTEGRATION=1 go test ./internal/deploy -run TestDockerComposeRollbackIntegration`.
The test creates disposable containers and verifies that rollback and cleanup
preserve named volume data.
