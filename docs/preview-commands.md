# Pull request deployment commands

An event rule listens for a command such as `/preview` on a pull request. A trusted repository member can start a deployment. Dispatch checks out the pull request revision, runs its hooks, deploys the application and updates one status comment.

When webhooks are unavailable, Dispatch polls repository issue comments every 30 seconds. It saves a cursor per repository, processes matching commands and checks active pull requests for closure. Webhooks and polling use the same comment ID to avoid starting a command twice.

## Link changes across repositories

Preview groups connect components stored in different repositories. A command can select another open pull request:

```text
/preview
/preview with ui=#123
/preview with owner/repository=#123
```

Dispatch keeps the deployment running until all explicitly linked pull requests close. An operator can also remove it from the console. The status comment lists the deployment URL and full commit ID for each linked component.

Hooks receive `DISPATCH_EVENT_*` variables for the triggering event and `DISPATCH_COMPONENT_<ALIAS>_<OUTPUT>` variables for available component outputs. Write named results with `dispatch-hook`, or use `DISPATCH_OUTPUT_FILE` for legacy scripts.

## Set Helm values from a comment

Use `/preview values` to supply Helm values for a deployment, or `/preview values clear` to remove them. Values survive source relinking, redeployment and controller restarts. Each workflow revision saves its own values, so a later comment cannot change a queued or running revision.

See [Helm values from PR comments](application-config.md#helm-values-from-pr-comments) for the syntax, including how to select a deployment.

## Save an inline Application

Choose One-off PR preview when adding an application and save the Application YAML, GitHub App, pull request, command and deployment URL. This creates a temporary resource without requiring a configuration repository commit. Repository sync preserves that resource.

The API accepts the same configuration at `POST /api/v1/workflow/temporary-resources`. Send `configSourceId` and one complete Application YAML document. Pin the pull request source to its full head commit and choose a unique deployment name and Helm release.

Configure its trigger with `POST /api/v1/workflow/temporary-resources/{id}/preview-trigger`. Send:

- `githubAppId`
- `repository`
- `pullRequestNumber`
- `previewUrl`, an HTTPS URL

A trusted `/preview` comment starts the revision through the repository-wide poller. Repeated polls of that comment do not start another run.

Use `/preview with ui=#123` or `/preview with owner/repository=#123` to link another source. The link stays on the trigger. Later `/preview` comments refresh both pull request heads, Helm provenance includes both pull requests and cleanup waits until both close.

### Reuse a document for several pull requests

Put `__PREVIEW_ID__` in each identity value that needs to be unique, including the Application name, Helm release, workload names and labels. Send `previewId` with the pull request number.

Dispatch uses that ID when it is free. If a resource name or Helm release is already in use, it appends a short random suffix. Service and UI pull requests with the same number can then have separate deployments.

### Update an existing deployment

Call `PUT /api/v1/workflow/temporary-resources/{id}` with the complete `document`. The next run upgrades the same Helm release.

To add or change a trigger URL, call `PUT /api/v1/workflow/preview-triggers/{id}/url` with `previewUrl`.

## Report the result to GitHub

After a successful revision, the poller posts or updates one report with the GitHub App installation token. It retries failed reports and does not use `DISPATCH_GITHUB_TOKEN`.

For a repository-managed workflow, an owner can also call `POST /api/v1/workflow/revisions/{id}/preview-report` after the revision succeeds. Send `githubAppId`, `repository`, `pullRequestNumber` and the verified HTTPS `url`.

The report includes the target, deployment, full source commits and built image tags. If an enterprise GitHub App rejects comment writes, a configured `DISPATCH_GITHUB_TOKEN` can provide a fallback for this manual endpoint.

## Find the deployed Helm release

Every Helm install and upgrade records Dispatch application and deployment IDs on the release Secret and rendered resources. Pull request releases also record linked pull request numbers and repositories, workflow resource and revision IDs, and pinned source commits in the `dispatch.app/provenance` annotation and Helm release description.

The `dispatch.app/has-pr=true` label on Helm release Secrets can help find orphaned releases if automatic cleanup misses one.

For expiry, closure checks and cleanup recovery, see the [preview lifecycle guide](preview-lifecycle.md).
