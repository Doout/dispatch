# Services

A Service registers a connection to an existing dependency. Dispatch does not create or delete that dependency. PostgreSQL may run on a VM, in Kubernetes, or with a cloud provider; registration is the same.

## Register and test

Open **Services > Register service**, choose a project, and select PostgreSQL or Generic. Project admins and operators can register connections. Viewers can inspect metadata, and deployers can run applications with existing bindings.

PostgreSQL accepts a URL or individual fields: `host`, `port`, `database`, `username`, `password`, `sslmode`, and optional PEM `caCert`. The default port is 5432 and the default TLS mode is `verify-full`. Supported explicit alternatives are `verify-ca`, `require`, and `disable`. URL query options are limited to `sslmode`; supply a CA certificate through `caCert`. The generated `connectionUrl` correctly escapes usernames, passwords, and database names. When a private CA is required, bind `caCert` separately using the application's supported certificate configuration.

Generic services expose named string fields. Mark credentials sensitive. A URL containing a password is automatically sensitive. An optional host and port enables a TCP reachability check.

Sensitive fields are encrypted, write-only, and separate from controller-wide secrets. An omitted field is preserved during updates. To clear a field, submit `{ "remove": true }`; supplying an empty string explicitly replaces the value with an empty string. Only the controller owner can attach global secret references, including external secret references. Operators can preserve or remove an existing reference or replace it with a local value.

**Test connection** runs from the Dispatch controller with a ten-second timeout. PostgreSQL checks authentication and executes `SELECT 1`; generic checks establish a TCP connection. Results show the check location, time, and duration. They do not prove application-network reachability or continuous health. A private service can be registered even when the controller cannot reach it.

## Bind applications

Open an application's **Services** action and choose **Connect service**. Give the dependency an alias and select fields to deliver:

- Dockerfile applications map fields to environment variables. Credentials are supplied only at container startup through a protected temporary environment file. Newlines and NUL are not supported by this delivery format.
- Compose applications name each receiving Compose service and its environment variables. Dispatch generates a protected override file, escapes interpolation, and leaves unselected containers unchanged. Unknown Compose service names fail before `compose up`.
- Helm applications map fields to Kubernetes Secret keys, then map the Secret name and optional key names to chart value paths. Paths use dotted object keys; array-index destinations are not supported. The chart must support existing Secret references. Raw credentials are never inserted into Helm values.

Service mappings override their configured runtime destinations. Duplicate or overlapping destinations, including conflicts with workflow job output bindings, are rejected. Bindings are project-scoped. Service credentials are not automatically available to image builds, workflow jobs, hooks, or preview templates.

Saving a service or binding does not restart applications. Connection changes increment the service revision and reset the check result. The Services page shows affected applications as **Redeployment required** by comparing their current bindings with their latest successful deployment.

Dispatch snapshots bindings and encrypted local service values when it accepts a deployment. External references resolve once before runtime changes. A later edit does not change the captured local values for an accepted deployment. External rotations take effect on the next deployment; they are not polled automatically.

Each Helm deployment creates a separate immutable Kubernetes Secret. Previous Secrets remain available for release history and rollback, including after a failed upgrade. Removing the release cleans up only Secrets labeled as owned by Dispatch for that application and release. Service registrations cannot be removed while bound, referenced by repository configuration, or used by an active deployment.

## Repository-managed applications

Register the services in Dispatch first. Add bindings under a deployment and override service selection by alias in each stage. Names resolve only within the configuration source's project. Defaults and every effective stage binding are validated before a configuration sync is accepted.

```yaml
apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: orders
spec:
  sources:
    chart:
      repository: example/deployment-config
      branch: main
  deployments:
    api:
      serviceBindings:
        - alias: database
          serviceRef: orders-db-development
          helm:
            keys:
              url: connectionUrl
            secretNameValues:
              - database.existingSecret
            keyValues:
              database.urlKey: url
      helm:
        sourceRef: chart
        chartPath: charts/orders
        namespace: orders
        releaseName: orders
  stages:
    - name: development
      targetRef: development
      deploy: [api]
    - name: production
      targetRef: production
      deploy: [api]
      approval: required
      serviceBindings:
        database: orders-db-production
```

The stage changes the service, keeping the deployment's field mappings. Generated applications display these bindings read-only. Edit the repository to change them.

## API examples

Register a service using ordinary fields and an encrypted password:

```json
{
  "projectId": "project-id",
  "name": "orders-db-development",
  "type": "postgresql",
  "fields": {
    "host": { "value": "postgres.internal" },
    "database": { "value": "orders" },
    "username": { "value": "orders" },
    "password": { "value": "replace-me" }
  }
}
```

Send this to `POST /api/v1/services`. A response contains `id`, `revision`, ordinary field values, sensitive-field configuration flags, available field names, and consumers. It never returns credentials. Send the current `revision` with updates to detect stale edits.

`PUT /api/v1/apps/{id}/service-bindings` accepts the complete binding list. A Dockerfile binding is:

```json
[{ "alias": "database", "serviceRef": "service-id", "environment": { "DATABASE_URL": "connectionUrl" } }]
```

A Compose binding is:

```json
[{ "alias": "database", "serviceRef": "service-id", "compose": { "api": { "DATABASE_URL": "connectionUrl" } } }]
```

Supply `[]` to remove all configured bindings. Existing containers change only after a new deployment.

Working [Dockerfile, Compose, and Helm examples](../examples/services/README.md) are included in this repository.

## Runtime integration tests

Use disposable infrastructure. The integration tests are opt-in:

- `DISPATCH_SERVICES_POSTGRES_URL`: PostgreSQL reachable from the test process, for authentication checks.
- `DISPATCH_SERVICES_STORE_POSTGRES_URL`: a separate empty PostgreSQL database, for migration and persistence tests.
- `DISPATCH_SERVICES_DOCKER_POSTGRES_URL`: PostgreSQL reachable from Docker's default network.
- `DISPATCH_SERVICES_TEST_KUBECONFIG` and `DISPATCH_SERVICES_KUBE_POSTGRES_URL`: a disposable Kubernetes cluster and PostgreSQL reachable from its pods.

The runtime tests use `postgres:17-alpine`. Load it into a local cluster before running. Run `go test ./internal/serviceconn ./internal/store ./internal/deploy -run 'Integration' -v`. Dockerfile and Compose fixtures execute `SELECT 1` using the injected URL. The Helm fixture becomes ready only after connecting, then tests an upgrade and owned-Secret cleanup. Tests create uniquely named application containers and Kubernetes namespaces and remove them afterward. They do not remove the supplied database or cluster.
