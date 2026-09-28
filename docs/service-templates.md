# Service templates

A `ServiceTemplate` defines the information Dispatch needs to create a Service. The provisioner owns how the resource is created. Applications see the same PostgreSQL or generic Service fields regardless of whether the provider is Docker, an existing cluster, or a cloud API.

Open **Services → Templates → Create template** to save a template in Dispatch. The form defines its inputs, provisioner script, and outputs. Switch to YAML for repository sources, secret references, and other job settings. An inline script does not need a repository connection. If the template fetches sources, select a repository configuration in the same project for access.

You can also place a `ServiceTemplate` YAML file inside a synced repository configuration path. The Templates tab lists both kinds and labels repository templates as GitOps. Edit or delete saved templates in Dispatch; change GitOps templates in their source repository. Deleting a template leaves its existing Services and run history in place. The YAML view uses the same document format, so you can copy a saved definition into a repository for version control.

To provision a resource, open **Services → Add service**, choose a template, and fill in its inputs. Dispatch saves the returned connection fields as a Service. Saving a template or syncing its repository does not run the provisioner.

## Contract

```yaml
apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: postgresql-docker
spec:
  description: New PostgreSQL container on a configured Docker host
  serviceType: postgresql
  inputs:
    database:
      label: Database name
      type: string
      required: true
    host:
      label: Reachable Docker host
      type: string
      required: true
  sources:
    provisioner:
      repository: example/infrastructure
      branch: main
  provision:
    runFrom: provisioner
    run: ./scripts/create-postgres-container.sh
  outputs:
    host: {}
    port: {}
    database: {}
    username: {}
    password:
      sensitive: true
    sslmode: {}
```

The script receives `DISPATCH_INPUT_DATABASE` and `DISPATCH_INPUT_HOST`. It writes one `name=value` line for each declared output to `DISPATCH_OUTPUT_FILE`. For example, its final step may write:

```sh
printf 'host=%s\nport=%s\ndatabase=%s\nusername=%s\npassword=%s\nsslmode=disable\n' \
  "$host" "$port" "$database" "$username" "$password" > "$DISPATCH_OUTPUT_FILE"
```

The provider script is responsible for starting the container, selecting an unused port, setting credentials, and ensuring the address is reachable by consumers. A Docker builder can be selected with `provision.builder: docker`; otherwise the command runs on the controller. Do not print credentials in provisioner logs.

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

Input types are `string`, `secret`, and `service`. A `service` input currently accepts a PostgreSQL Service in the same project. Input values are supplied to the job as environment variables and are not saved in provision run records. Output values are not saved in ordinary workflow job history; sensitive Service fields are encrypted before storage. The run record only stores status and a generic failure message.

Provisioning can have external side effects. An interrupted or failed run is never retried automatically. Check the provider before starting another run, and give each request a unique Service name. Removing a Service registration does not delete its provider resource.

## API

`GET /api/v1/service-templates` lists visible saved and GitOps templates. `GET /api/v1/service-templates/{id}` includes the YAML document. A template's `managedBy` is `dispatch` or `gitops`.

Create a saved template with `POST /api/v1/service-templates` using `projectId`, `document`, and an optional `configSourceId`. Update it with `PUT /api/v1/service-templates/{id}` using the same fields and its current `revision`. Delete it with `DELETE /api/v1/service-templates/{id}?revision=N`. A stale revision returns 409. These writes require direct user authentication and `project.configure`; GitOps templates cannot be changed through these endpoints.

`POST /api/v1/service-templates/{id}/runs` provisions a Service from either kind of template. It requires both `project.configure` and `deployment.run`.
