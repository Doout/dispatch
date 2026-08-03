# Dispatch

Dispatch is a lightweight, self-hosted deployment control plane for personal infrastructure. It pairs a Go controller and embedded React console with a small node agent, a Docker-first runtime driver, and versioned provider interfaces for private cloud automation.

> **Status:** private, early implementation. Do not expose the controller or agent to untrusted networks yet.

## What works in the first milestone

- SQLite by default, with a PostgreSQL storage adapter selected by `DATABASE_URL`.
- Projects, Docker servers, applications, and immutable deployment records.
- A deployment queue with explicit stages and server-sent event updates.
- Dockerfile and Compose application specifications.
- Provider and runtime interfaces prepared for private sidecars and Kubernetes.
- A responsive deployment-dispatch console embedded into the Go binary.

The Docker executor is deliberately capability-gated. Development/demo mode can exercise the complete state machine without mutating Docker; live Docker execution is enabled only with `DISPATCH_EXECUTOR=docker`.

## Development

Requirements: Go 1.26+, Node.js 22+, Corepack, and Docker for live executor testing.

```bash
corepack pnpm --dir web install
corepack pnpm --dir web build
go test ./...
go run ./cmd/dispatch
```

Open <http://localhost:8080>. Set `DISPATCH_DEMO=true` to load clearly labeled local demonstration records.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `DISPATCH_ADDR` | `127.0.0.1:8080` | Controller listen address |
| `DATABASE_URL` | `dispatch.db` | SQLite path or PostgreSQL URL |
| `DISPATCH_EXECUTOR` | `simulation` | `simulation` or explicitly enabled `docker` |
| `DISPATCH_DEMO` | `false` | Seed local demo records when the store is empty |
| `DISPATCH_ADMIN_TOKEN` | none | Bearer token; required when binding beyond loopback |
| `DISPATCH_MASTER_KEY_FILE` | none | File containing the 32-byte secret-encryption key |

Schema changes live as ordered SQL files under `internal/store/migrations`; Go only discovers and applies them. See [docs/architecture.md](docs/architecture.md) for system boundaries, [docs/openapi.yaml](docs/openapi.yaml) for the controller contract, [docs/provider-api.md](docs/provider-api.md) for the private provider contract, and [docs/roadmap.md](docs/roadmap.md) for staged scope.

## Privacy and licensing

The GitHub repository is private until its owner explicitly approves publication. The code is prepared under Apache-2.0 so a later public release has a clear license boundary. Private provider implementations and credentials do not belong in this repository.
