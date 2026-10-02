# Infrastructure providers

Open **Servers → Infrastructure providers → Register provider** as the controller owner. Supply the adapter endpoint, choose direct access or an existing edge node, and select a credential from **Secrets**. Dispatch stores the secret reference. Credential values remain write-only and are resolved only for a provider request.

HTTPS is required. A direct loopback endpoint may use HTTP for the local mock. A private route always uses the selected edge node and verified HTTPS. There is no direct fallback when that node is unavailable.

The registration stores the verified `dispatch.provider/v1` manifest, its digest, approved capabilities and a revision. Invalid schemas, incompatible versions and missing capabilities leave the registration disabled with a failure message. JSON Schema validation does not load files or external references. Configuration discovery rejects top-level fields absent from the manifest's `properties` map, even when the provider schema otherwise allows additional properties.

Endpoint and network route are fixed for a registration. Register a new connection to point at another adapter. Verification may accept a new implementation version or schema after review, but the adapter name and API version must keep their original identity. Create submissions retain their reviewed manifest digest and original registration. Existing resources can be inspected or deleted after an approved compatible upgrade, with their original ownership labels.

To rotate authentication, update the existing secret in **Secrets**, then verify the provider. To use another credential, edit the registration and choose the new secret reference. A referenced credential cannot be deleted or converted to a plain variable. Its provider reference appears in secret usage.

**Disable provider** prevents new create and delete operations. It retains the registration and allows approved inspection. It does not remove cloud resources. Re-enabling verifies the connection before approving mutations.

## API

The registration endpoints below require a controller owner. Project operators and automation accounts use the assigned-provider catalog and lifecycle APIs described in [on-demand servers](on-demand-servers.md), subject to current grants and quotas.

| Method | Path | Result |
| --- | --- | --- |
| GET | `/api/v1/infrastructure/providers` | Registration metadata and verified schemas |
| POST | `/api/v1/infrastructure/providers` | Save and verify a registration |
| PUT | `/api/v1/infrastructure/providers/{id}` | Update name, credential reference, capabilities or enabled state using the current `revision` |
| POST | `/api/v1/infrastructure/providers/{id}/verify` | Recheck the manifest using `{"revision": N}` |
| POST | `/api/v1/infrastructure/providers/{id}/options` | Discover options using `{"kind":"regions","config":{}}` |

Create and update accept `name`, `endpoint`, `privateNetworkId`, `credentialSecretId`, `enabled` and `capabilities`. Updates also require the current revision. The API returns no credential values and accepts no inline provider authentication token. A saved registration with `state: "failed"` needs operator attention before it can be enabled.

For a credential-free development adapter, run the [persistent mock provider](provider-api.md), register `http://127.0.0.1:8091`, choose **No authentication**, and approve `server.inspect`, `server.create` and `server.delete`. This registers the adapter; it does not allocate a server.

## Verification

Focused tests cover SQLite restart persistence, credential rotation, disablement, invalid manifests and schemas, private edge routing, rejection of unknown configuration fields, credential redaction, API ownership and UI credential references. Run the PostgreSQL round-trip test against an empty disposable database with `DISPATCH_PROVIDER_POSTGRES_URL`:

```sh
go test -race -p 2 ./internal/provision ./internal/provider/...
go test -p 2 ./internal/store -run TestInfrastructureProvider
```
