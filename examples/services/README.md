# PostgreSQL service connection examples

These applications check a database that already exists. They do not provision PostgreSQL or change its data.

1. Register a PostgreSQL service in Dispatch with an endpoint reachable from the chosen deployment target. Use credentials for a test database. The controller's connection check runs from a different location than the application.
2. Create an application using this repository and one of the definitions below. Keep it in the service's project.
3. Open the application's **Services** action, add the binding, then deploy. The application executes `SELECT 1` and remains running after success.

| Deployment | Source configuration | Binding |
| --- | --- | --- |
| Dockerfile | Context `examples/services`, Dockerfile `examples/services/Dockerfile` | Map service field `connectionUrl` to environment variable `DATABASE_URL`. |
| Compose | Compose path `examples/services/compose.yaml` | Select Compose service `check` and map `connectionUrl` to `DATABASE_URL`. The `unrelated` container receives no database credentials. |
| Helm | Chart path `examples/services/chart` | Map `connectionUrl` to Secret key `url`; map the Secret name into `database.existingSecret` and key name `url` into `database.urlKey`. |

The Helm application becomes ready only after the database query succeeds. Use the application's logs to inspect errors. Choose a PostgreSQL TLS mode supported by your test endpoint; certificate and hostname verification is the default.

For stage-specific databases and API request examples, see [services](../../docs/services.md).
