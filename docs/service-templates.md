# Service templates

A `ServiceTemplate` defines the information Dispatch needs to create a Service. The provisioner owns how the resource is created. Applications see the same PostgreSQL or generic Service fields regardless of whether the provider is Docker, an existing cluster, or a cloud API.

Open **Services → Templates → Create template** to save a template in Dispatch. Choose Docker or Helm and select a deployment server. PostgreSQL uses built-in defaults for credentials, storage, and readiness. Image, network, and storage settings are optional. Custom scripts are an advanced provider option. Switch to YAML for custom charts, images, input mappings, or repository sources. Built-in providers do not need a repository connection. If the template fetches sources, select a repository configuration in the same project for access.

You can also place a `ServiceTemplate` YAML file inside a synced repository configuration path. The Templates tab lists both kinds and labels repository templates as GitOps. Edit or delete saved templates in Dispatch; change GitOps templates in their source repository. Deleting a template leaves its existing Services and run history in place. The YAML view uses the same document format, so you can copy a saved definition into a repository for version control.

To provision a resource, open **Services → Add service**, choose a template, and fill in its inputs. Dispatch saves the returned connection fields as a Service. Saving a template or syncing its repository does not run the provisioner.

## Docker

A PostgreSQL template only needs a server reference. The UI saves the server ID; use that ID in repository YAML as well.

```yaml
apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: postgresql-docker
spec:
  serviceType: postgresql
  provision:
    docker:
      serverRef: local-server-id
```

Dispatch starts `postgres:17-bookworm`, generates a password, creates a labeled persistent volume, and waits for the container's health check. The Service name becomes the database name, and the database user is `dispatch`. Add optional inputs named `database` or `username` to change these per request. The image, `network`, and `storageMountPath` can be configured under `docker`.

The default network is `dispatch-services`. The database publishes no host port; connect applications to this Docker network. The saved host is the unique container name, with port 5432 and `sslmode=disable`. Docker currently requires a local enrolled deployment server. Builder servers run builds and cannot host these services. Each container gets 512 MiB memory and 0.5 CPU.

The image must support the official PostgreSQL initialization variables. Dispatch sets `PGDATA` explicitly to `/var/lib/postgresql/data/pgdata`; the default volume mounts at `/var/lib/postgresql/data`. See the [official image documentation](https://hub.docker.com/_/postgres) before changing the image or storage layout.

## Helm

```yaml
apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: postgresql-helm
spec:
  serviceType: postgresql
  provision:
    helm:
      serverRef: cluster-server-id
      namespace: databases
```

Dispatch installs its bundled PostgreSQL chart with the existing Helm SDK. It creates a Secret, a 10 GiB persistent volume claim, a StatefulSet, and an internal ClusterIP Service. Helm waits for readiness and rolls back failed installs. Release metadata links the resource to its template and provision run. The database name and user follow the same defaults as Docker.

The namespace defaults to the server's configured namespace, then `default`. Optional settings are `image`, `storage`, and `storageClass`. The cluster must have a storage provisioner, or specify an available storage class. Applications connect through the saved internal cluster address. The default chart does not configure TLS or external access. It requests 100m CPU and 128 MiB memory, with limits of 500m CPU and 512 MiB memory.

For another chart, configure `chart` as an OCI or HTTPS reference, or pair a chart name with an HTTPS `repository`. Set `version`, `values`, and `connection` mappings to match that chart. No provider code is required. The chart owns how its resources are installed and exposes the same Service fields.

## Inputs and outputs

PostgreSQL defaults to a sensitive `connectionUrl` output. You can declare individual connection fields instead. Generic services must declare their outputs and a `connection` mapping for each field. Generic Docker services also need an image and an explicit `healthcheck` argument list; generic Helm services need a chart.

Declarative Docker environment values, healthcheck arguments, Helm values, and connection mappings accept these references:

| Reference | Value |
| --- | --- |
| `{{ inputs.name }}` | A declared template input |
| `{{ service.name }}` | The requested Service name |
| `{{ service.resource }}` | Unique container or release name |
| `{{ service.namespace }}` | Helm namespace |
| `{{ service.database }}` | Input named database, or the Service name |
| `{{ service.username }}` | Input named username, or dispatch |
| `{{ service.password }}` | Password generated for this run |

Values are passed as data. Inputs do not become shell commands or YAML fragments. Mark credential outputs sensitive. Helm stores rendered Secrets and supplied values in its release records; cluster access controls protect those credentials.

```yaml
# Example fragment for a chart whose values and connection follow this schema.
provision:
  helm:
    serverRef: cluster-server-id
    chart: postgresql
    repository: https://charts.example.test
    version: 1.2.3
    values:
      auth:
        database: '{{ service.database }}'
        username: '{{ service.username }}'
        password: '{{ service.password }}'
    connection:
      connectionUrl: 'postgresql://{{ service.username }}:{{ service.password }}@{{ service.resource }}.{{ service.namespace }}.svc:5432/{{ service.database }}?sslmode=disable'
```

## Custom providers

Existing script templates continue to work. A script receives declared inputs as `DISPATCH_INPUT_NAME` environment variables and writes `name=value` lines to `DISPATCH_OUTPUT_FILE` for each declared output. Use scripts for providers that expose a separate provisioning API or administrative SQL operation.

To create a database inside an existing PostgreSQL cluster, change the inputs and script while keeping the same outputs:

```yaml
apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: postgresql-database
spec:
  description: New database in an existing PostgreSQL cluster
  serviceType: postgresql
  inputs:
    cluster:
      label: PostgreSQL cluster
      type: service
      serviceType: postgresql
      required: true
    database:
      label: Database name
      type: string
      required: true
  sources:
    provisioner:
      repository: example/infrastructure
  provision:
    runFrom: provisioner
    run: ./scripts/create-postgres-database.sh
  outputs:
    host: {}
    port: {}
    database: {}
    username: {}
    password:
      sensitive: true
    sslmode: {}
```

The second script receives `DISPATCH_INPUT_CLUSTER` as a resolved PostgreSQL URL and `DISPATCH_INPUT_DATABASE` as the requested database name. It creates the database using the cluster's administrative connection and returns the new database's connection fields. A managed cloud database provisioner can use the same Service contract with different inputs, source code, and command.

Input types are `string`, `secret`, and `service`. A `service` input currently accepts a PostgreSQL Service in the same project. Input values are supplied to the job as environment variables and are not saved in provision run records. Output values are not saved in ordinary workflow job history; sensitive Service fields are encrypted before storage. Run records store status, a safe failure message, and built-in resource identities. They never store generated passwords or resolved inputs.

Provisioning can have external side effects. An interrupted or failed run is never retried automatically. Check the provider before starting another run, and give each request a unique Service name. Removing a Service registration does not delete its provider resource. Failed Docker readiness checks remove the candidate container but retain its labeled data volume. The bundled Helm chart retains its PVC on uninstall, including rollback. Inspect and remove retained storage explicitly when it is no longer needed.

## API

`GET /api/v1/service-templates` lists visible saved and GitOps templates. `GET /api/v1/service-templates/{id}` includes the YAML document. A template's `managedBy` is `dispatch` or `gitops`.

Create a saved template with `POST /api/v1/service-templates` using `projectId`, `document`, and an optional `configSourceId`. Update it with `PUT /api/v1/service-templates/{id}` using the same fields and its current `revision`. Delete it with `DELETE /api/v1/service-templates/{id}?revision=N`. A stale revision returns 409. These writes require direct user authentication and `project.configure`; GitOps templates cannot be changed through these endpoints.

`POST /api/v1/service-templates/{id}/runs` provisions a Service from either kind of template. It requires both `project.configure` and `deployment.run`.
