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
the project, application configuration digest, binding mapping digest, and service
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
missing credentials produce an explicit unavailable reason. Docker and Compose
artifacts are not retained for historical rollback in this release.

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
| `POST /api/v1/deployments/{id}/rollback` | `deployment.run`; requires `confirmDeploymentId`, `expectedCurrentDeploymentId`, and `confirmDatabaseNotReverted: true`. |
| `GET /api/v1/deployments/{id}/diagnosis` | `project.view`; bounded current workload observation and sanitized log excerpts. |

`POST /api/v1/apps/{id}/deployments` accepts the optional `review` object returned
by preview alongside `commitSha`. An incomplete review returns 422; changed
reviewed inputs return 409 and do not create a deployment. Existing clients can
continue to create an ordinary deployment without the review object.

SQLite and PostgreSQL migration `042_release_tools` stores notes and release
actions. Existing applications require no data conversion. The runtime integration
test uses a disposable cluster through `DISPATCH_RELEASE_KUBECONFIG`; persistence
also supports `DISPATCH_RELEASE_POSTGRES_URL` pointing to a disposable database.
