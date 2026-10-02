# Neon preview databases

Dispatch can create an isolated PostgreSQL branch in an existing Neon project.
It does not install or provision a Neon cluster. The initial adapter supports
schema-only preview branches, read-write compute, branch-specific credentials,
reconciliation and reviewed branch deletion. Live Neon account validation is
pending; conformance tests use local TLS fixtures matching the published API.

## Configure a provider and service template

In **Services → Templates → Create template**, choose **Neon**. A controller
owner can add a project provider by selecting an encrypted Neon API key and
entering its API endpoint, Neon project ID and parent branch ID. Use a key
restricted to that Neon project. Defaults use
`https://console.neon.tech/api/v2`; compatible endpoints must use HTTPS and may
not redirect. The saved endpoint, credential reference and Dispatch project
assignment are immutable. Register a new provider to change them.

The same configuration is available through owner-only
`POST /api/v1/neon-providers`; project members can list assigned providers with
`GET /api/v1/neon-providers?projectId=PROJECT_ID`. The API never returns a token.
A saved or repository `ServiceTemplate` references that assigned provider:

```yaml
apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: preview-postgres
spec:
  serviceType: postgresql
  provision:
    neon:
      providerRef: PROVIDER_ID
      database: neondb
      dataMode: schema-only
      suspendAfterSeconds: 300
  outputs:
    connectionUrl: {sensitive: true}
```

The selected database must exist on the parent. Schema-only copies its schema,
without parent rows. Dispatch creates a new deterministic role on the owned
branch and refuses to reuse a role that exists on the source. It requests the
connection URI with an explicit branch, compute, database and role; provider
defaults cannot select the production connection. The accepted API key and
returned connection are encrypted. Provider response bodies are not included
in errors, logs or public operation records.

A manual service provision may use `parent-data` only when a controller owner
submits `confirmDataCopy: "Copy parent rows into SERVICE_NAME"` with the named
provision request. Automatic preview databases reject that mode. No masking is
performed by Dispatch; a separate, reviewed masked seed process is required
before any copied rows are suitable for preview use.

## Bind a database to a preview

Declare `previewServices` in the Application rendered by your WorkflowTemplate.
Use a ServiceTemplate ID from the same Dispatch project. The controller prepares
the database after checking the complete preview source revision and before
running jobs. The source approval includes the selected template digest and
provider identity. Fork approvals cannot authorize a later template change.

```yaml
spec:
  previewServices:
    database:
      templateRef: SERVICE_TEMPLATE_ID
      cleanupPolicy: retain
  sources:
    app:
      repository: example/application
  jobs:
    migrate:
      runFrom: app
      run: ./scripts/migrate.sh
      reuse: never
      secrets:
        DATABASE_URL:
          serviceRef: preview:database
          key: connectionUrl
  deployments:
    app:
      helm:
        sourceRef: app
        chartPath: charts/app
        releaseName: my-preview
        namespace: my-preview
      serviceBindings:
        - alias: database
          serviceRef: preview:database
          helm:
            keys:
              DATABASE_URL: connectionUrl
            secretNameValues: [database.existingSecret]
  stages:
    - name: preview
      targetRef: preview-cluster
      approval: automatic
      deploy: [app]
```

Merge this fragment with your existing preview template and its instance naming.
`DATABASE_URL` is available only to the declared job. Normal job log redaction
applies. Do not echo credentials or write them to declared job outputs. A failed
migration job prevents all deployment stages, leaving the prior deployment in
place. Use expand-and-contract migrations: deployment rollback reuses the branch
and does not undo database schema or data changes.

One durable link connects each preview resource and alias to its original
ServiceRun. New commits and controller restarts reuse that branch and its rows.
Changing the referenced template configuration requires a reviewed replacement;
Dispatch does not silently recreate or change an existing preview database.
Stable production and staging services remain ordinary project service bindings
and can coexist with these preview bindings.

## Recovery and deletion

Owned-service history shows the provider phase, preview identity and safe
recovery timing. The accepted create checkpoint is saved before a provider POST.
After a lost response, reconciliation searches the exact deterministic branch
name and verifies its Dispatch project, run, preview, configuration and generation
annotations. It completes missing compute and role setup on that branch. It
never repeats branch creation after an uncertain create whose branch cannot be
found. Inspect the original run and provider; do not repeatedly submit new runs.

Source/default/protected branches and branches with changed ownership are refused.
Active preview references and application consumers block deletion. Preview close,
expiry and ordinary application cleanup retain the database. Neon idle compute
suspension can reduce idle compute use, but a new connection wakes it; this is
not a hard stop or database deletion policy.

After stopping the preview and detaching its deployed consumers, use the existing
owned-service **Delete resource** action. Its typed confirmation explicitly
reviews permanent branch and data deletion. Reconciliation of an interrupted
delete inspects the same captured branch identity. Other branches, the parent
and the Neon project remain untouched.

## Current limits

`retain` is the only automatic cleanup policy in this initial adapter. Automatic
TTL branch deletion and reviewed reset/replacement operations remain follow-up
work. To start with a fresh schema, create a new preview identity, test its new
connection and migrations, then review deletion of the old branch. Neon
schema-only branches are independent roots; invoking the provider restore API
would copy schema **and data**, so Dispatch does not expose that as a safe reset.

Native Dispatch workload backups currently support owned PostgreSQL/Docker
services, not Neon branches. Use the provider's supported recovery facilities
and independently verify your recovery procedure before explicitly deleting
valuable branch data. Retained deployment artifacts do not back up this database.

Provider reference: [Neon API](https://neon.com/docs/reference/api),
[published API schema](https://neon.com/api_spec/release/v2.json), and
[schema-only branching](https://neon.com/docs/guides/branching-schema-only).
