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

Run `make web` after frontend changes so the controller includes the updated assets. Add tests for changed behavior, especially deployment retries, permissions, and recovery. Add new database migrations instead of editing migrations that have shipped.

Explain the problem, the resulting behavior, and how you checked it. Include screenshots for visible interface changes. Keep unrelated edits in separate pull requests.

Use example domains and fake credentials in fixtures. Do not attach controller databases, kubeconfigs, environment files, or unedited deployment logs to an issue or pull request. See [security reporting](SECURITY.md) for vulnerabilities.

Contributions use the repository's [Apache 2.0 license](LICENSE).
