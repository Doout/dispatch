# Variable usage

Open **Variables** and select **Used by** on a value to see its configured consumers. The lookup includes workflow jobs and finally jobs, JSON keys, preview and service templates, repository credentials, deployment hooks, service connection fields, and builder SSH credentials. Generated applications are included.

Each consumer counts once. A workflow referencing two JSON keys or the same credential in several jobs counts as one consumer; its list shows every reference. Paused configurations still count because they retain the reference. Removed workflows and closed applications appear under **Archived references**.

Related applications link to their deployment history. **Show deployments** loads summaries on demand, with **Load more** for older runs. This history shows deployments of referenced applications. It does not prove that today's references were present in every past revision or that a secret was read in every run.

While the lookup is loading, the page shows **Checking usage**. Failed requests show **Usage unavailable** with a retry action. Unreadable YAML produces **Usage incomplete** and identifies the affected configuration. These states never display a confirmed zero.

The lookup reads reference metadata without resolving or decrypting values. Both usage APIs require the controller owner role:

- `GET /api/v1/secrets/usage` returns each value's `secretId`, `consumers`, `archived`, and `warnings`.
- `GET /api/v1/secrets/{id}/usage/deployments` returns at most 50 summaries in `items` and an optional `next` cursor. Pass that cursor as `before` for the following page.
