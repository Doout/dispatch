# PR preview lifecycle

PR previews deploy on an explicit comment by default. Automatic deployment and live reload are opt-in. Automatic runs retain their rolling hourly limit and cancel superseded work. A TTL of `0` remains unlimited.

Webhook and polling deliveries use durable comment receipts and one active template binding for each GitHub App, repository, PR, and command. A delayed commit event resolves the current PR head before selecting sources. Replaying a command from a removed binding cannot create a replacement. A newer comment may create a new instance. A completed expired instance keeps its binding and can be renewed by a new comment.

Closure rechecks the primary PR and every linked PR. Closing one PR keeps the preview while another remains open. A delayed closure received after a reopen cannot remove the current preview. GitHub lookup failures keep the preview until the next successful reconciliation.

Helm values submitted with `/preview values` belong to that preview's deployment.
They survive source relinking, manual redeployment, automatic updates and controller
restarts. A newer values comment replaces the overrides for its selected
deployment; `/preview values clear` removes them. Each workflow revision captures
its own values, so a later comment cannot change a queued or running revision.
Existing run and deployment history retains those snapshots after cleanup. See
[the comment syntax](application-config.md#helm-values-from-pr-comments).

## Cleanup acceptance and recovery

Explicit removal, expiry, and final PR closure save a cleanup intent before cancelling work or touching the target. The intent captures generated applications owned by the preview, their target and specification, and one stable cleanup operation per application. Same-project legacy stage history can identify older generated applications; cross-project or conflicting ownership stops cleanup.

Accepted cleanup prevents new workflow revisions, generated applications, deployments, and runtime mutation leases. Cancelled workers cannot report a late success that revives the preview. The controller waits for its cancelled deployment executors before teardown. Remote operations already delivered to an agent become uncertain and require inspection and acknowledgment after their old lease expires. The controller never assumes cancellation undid an external effect.

The independent cleanup timer runs every 30 seconds and does not depend on GitHub access. It claims a six-minute lease, works for at most five minutes, and saves per-application results. A replacement controller can resume after the lease expires. Completed applications are retained as closed history and are not removed again. Pending remote jobs keep their original operation ID; confirmed failures can start another attempt while retaining earlier IDs. A changed target enrollment blocks cleanup until operator recovery; the intent keeps its captured identity.

The preview inspector shows cleanup history, attempt counts, owned applications, and failure details. `DELETE /api/v1/workflow/temporary-resources/{id}` returns `204` after completion or `202` with the durable cleanup record while cleanup is pending or blocked. Repeating the request resumes the same intent. Inspect `WorkflowResource.previewCleanups` for progress. Terminal history remains available after the runtime has gone. A configuration source with owned previews or cleanup history can be disabled; deletion returns a conflict so it cannot erase that evidence.

## Data and source boundaries

Teardown uses the existing application cleanup path, including routing and storage ownership checks. It retains application and deployment records. Shared servers, shared services, retained volumes, and backup archives keep their existing ownership and retention policies. This controller does not introduce cascading data deletion.

Every new preview run still passes the source trust policy. Relinking or changing a source produces a new source snapshot and cannot reuse approval for a different revision. Cleanup does not rewrite the source commits or linked PR identities captured by earlier runs.
