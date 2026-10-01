# DevOps and agent client

`dispatchctl` is a thin API client and stdio MCP server. It returns the same reviewed operations and durable receipts as the browser and REST API. It cannot approve a human gate or override a project grant, provider assignment, quota, source trust decision or data protection rule.

Build with `go build -o dispatchctl ./cmd/dispatchctl`. Linux release archives include the client. Run `dispatchctl --help` for its supported commands. The underscore form, such as `deployment_preview`, is also accepted and matches the MCP tool name.

## Credentials

Have a controller owner create a service account with only the required project grants. Inspection needs `project.view`; deployment needs `deployment.run`. Infrastructure uses its separate inspect, create and delete grants and assigned providers and SSH public keys. An owner credential is not needed for ordinary automation. See [automation identities](automation-identities.md).

Save the issued token in a mode 0600 file. Keep the file out of Git and shell history. Configure only its path and the controller origin:

```sh
export DISPATCH_URL=https://dispatch.example.com
export DISPATCH_TOKEN_FILE=/run/secrets/dispatch-token
```

The client accepts HTTPS, with HTTP allowed for loopback development. Redirects are not followed. It never prints the authentication header and redacts an echoed token from responses. Each response is limited to 4 MiB; request files and MCP messages are limited to 1 MiB. The default HTTP timeout is 30 seconds; use the global `--request-timeout 60s` option before a command when needed.

## Inspect and deploy

Use an existing application whose target and credentials were assigned by its owner:

```sh
dispatchctl projects list
dispatchctl apps list --project PROJECT_ID
dispatchctl deployment preview --app APP_ID --revision FULL_COMMIT_SHA > preview.json
```

Read `preview.json`, including checks and the exact source revision. A preview does not approve a protected source or a pending human gate. Supply its review unchanged after the required approval:

```sh
jq '{commitSha: .data.revision, review: .data.review}' preview.json > deployment.json
dispatchctl deployment start --app APP_ID --key ci-release-2026-001 --input deployment.json > accepted.json
jq '.data | {id, operationId, state, recoveryActions}' accepted.json
```

Save the request file, application ID and key until its outcome is resolved. If the reply is lost, repeat that exact command and key. Do not create another key merely because a network request timed out. A changed request with the same key is rejected by the server.

```sh
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl deployment get --deployment DEPLOYMENT_ID
dispatchctl deployment logs --deployment DEPLOYMENT_ID --limit 100 --after 0
dispatchctl deployment diagnose --deployment DEPLOYMENT_ID
```

A wait timeout or Ctrl-C stops the client. It does not cancel accepted work. The result includes a `continuation` with the receipt or deployment ID. Resume with that ID. Log entry IDs are continuation cursors; pass the last received ID as `--after` for the next bounded page.

## Review, create and remove a server

The owner must register a provider, assign it and an SSH public key to the project, and set an allocation policy. Verify those prerequisites with `quota get --project PROJECT_ID`.

Prepare `server.json` using provider-supported choices:

```json
{
  "projectId": "PROJECT_ID",
  "providerId": "PROVIDER_ID",
  "name": "review-environment",
  "region": "mock-region",
  "size": "mock-small",
  "image": "mock-linux",
  "network": "mock-private",
  "sshKeySecretId": "ASSIGNED_SSH_KEY_ID",
  "config": {}
}
```

Use the public mock provider from [the provider API documentation](provider-api.md) for an allocation rehearsal without cloud credentials. Its server record is simulated and is not a real VM. Supported providers can request reviewed cloud-init through an optional `bootstrap` object with `method`, `platform`, `imageFamily` and `installRuntime`. The controller supplies and pins the installer artifact and identity.

```sh
dispatchctl server review --input server.json > server-review.json
```

Read the review before writing `accept-server.json` with its `reviewId`, `digest` and exact `confirmName`. The client does not fill in those confirmation fields.

```sh
dispatchctl server create --key create-environment-001 --input accept-server.json
dispatchctl receipt wait --receipt RECEIPT_ID --timeout 120
dispatchctl servers list --project PROJECT_ID
dispatchctl server operations --server SERVER_ID
```

Allocation success is separate from agent enrollment and workload readiness. Inspect the server states before deploying. A partial allocation can still consume quota. Follow the original operation's recovery state; do not create a replacement to hide an unresolved outcome.

After detaching workloads and addressing retained data, request a deletion review:

```sh
dispatchctl server delete review --server SERVER_ID > delete-review.json
```

Read the consequences and resolve any protected storage. Supply the reviewed `digest` and exact `confirmName` in `delete-server.json`, then:

```sh
dispatchctl server delete --server SERVER_ID --key delete-environment-001 --input delete-server.json
dispatchctl receipt wait --receipt DELETE_RECEIPT_ID --timeout 120
```

Snapshot and temporary-environment commands are added only when their controller APIs are integrated. The client exposes no arbitrary command execution, secret administration or human-approval tool.

## MCP

Launch `dispatchctl mcp` as a stdio server with the same scoped credential configuration. For clients using an `mcpServers` configuration:

```json
{
  "mcpServers": {
    "dispatch": {
      "command": "/usr/local/bin/dispatchctl",
      "args": ["mcp"],
      "env": {
        "DISPATCH_URL": "https://dispatch.example.com",
        "DISPATCH_TOKEN_FILE": "/run/secrets/dispatch-token"
      }
    }
  }
}
```

The transport follows the MCP [stdio](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports), [tool](https://modelcontextprotocol.io/specification/2025-11-25/server/tools) and [lifecycle](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle) contracts. It supports protocol 2025-11-25 and the compatible 2025-06-18 and 2025-03-26 versions. Only protocol messages go to stdout. Tool schemas restrict the supported operations; arguments cannot select an arbitrary URL or approval endpoint. Results include both structured JSON and its text representation. Cancellation stops the request or wait, while the server retains any accepted operation.

## JSON and exit codes

Every command returns `version: "dispatch.client/v1"`, `ok`, HTTP `status` when available, and `data` or a structured `error`. Receipts preserve resource and operation IDs, replay status, the retry deadline, recovery actions and server review references. Server problem details remain available in `error.evidence`.

| Exit | Meaning |
| --- | --- |
| 0 | Request succeeded, or wait reached a successful result or explicit paused state |
| 1 | Controller, protocol, capability, quota or other request failure |
| 2 | Invalid arguments or credential configuration |
| 3 | Authentication or project permission denied |
| 4 | Client timeout or cancellation, accepted work may continue |
| 5 | Operation failed, was cancelled or has an unresolved external outcome |

No convenience flag converts a paused approval into permission to execute. Use the server's review identifier and approval process, then inspect the same operation again.
