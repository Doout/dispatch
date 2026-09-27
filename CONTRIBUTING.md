# Contributing

Dispatch is a Go controller with an embedded React application. Start with the [architecture](docs/architecture.md) and [repository configuration](docs/application-config.md) docs when changing deployment behavior.

## Local setup

Use the Go version in `go.mod`, Node.js 22, Corepack, and Docker. The frontend uses the pnpm version pinned in the Makefile and CI.

```sh
make web
go run ./cmd/dispatch
```

Open `http://127.0.0.1:8080` and create the owner account. The default executor simulates deployments. Use a separate database and test infrastructure when enabling the Docker executor. Never point a development instance at production storage.

## Before opening a pull request

```sh
make check
sh scripts/install_test.sh
```

Run `make web` after frontend changes so the controller includes the updated assets. Add tests for changed behavior, especially deployment retries, permissions, and recovery.

Explain the problem, the resulting behavior, and how you checked it. Include screenshots for visible interface changes. Keep unrelated edits in separate pull requests.

Use example domains and fake credentials in fixtures. Do not attach controller databases, kubeconfigs, environment files, or unedited deployment logs to an issue or pull request. See [security reporting](SECURITY.md) for vulnerabilities.

## API routes

`internal/api/api.go` constructs the services. `routes.go` installs request
logging, public endpoints, authentication, and mutation auditing. The other
`routes_*.go` files register endpoints by resource. Handlers live in resource
files such as `applications.go`, `servers.go`, and `authentication.go`.

Add endpoints to the existing route group. Use Chi middleware for shared access
checks, such as `r.Use(a.ownerOnly)` on a group or
`r.With(a.appPermission(core.PermissionProjectView)).Get(...)` on one route.
Resource permission checks belong inside the `/{id}` group so the ID is
available when middleware runs. Keep public callbacks outside the authenticated
group and verify their webhook signature, OAuth state, or node credentials in
the handler.

## Database migrations

Each database dialect has one `migrations.sql` file under
`internal/store/migrations`. Append a section to both files using a new version:

```sql
-- dispatch:migration 057_describe_the_change
ALTER TABLE example ADD COLUMN new_value TEXT NOT NULL DEFAULT '';
```

Keep version names identical across dialects. Do not edit or renumber applied
sections. Each section runs in its own transaction and is recorded in
`schema_migrations`; existing databases skip completed versions. The section
boundary is the version marker, so SQL functions and triggers can contain
multiple statements. SQLite table rebuilds that need foreign keys disabled use
`-- dispatch:foreign-keys-off` within their section.

Run `go test ./internal/store` after schema changes. Set
`DISPATCH_TEST_POSTGRES_URL` to a disposable PostgreSQL database to exercise that
dialect too.

Contributions use the repository's [Apache 2.0 license](LICENSE).
