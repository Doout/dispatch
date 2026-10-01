# Independent provider packages

A provider is a separate HTTP service. Its source, private fixtures, cloud
credentials, build pipeline, registry, and release ownership stay in its own
repository. Dispatch registers an endpoint and a secret reference and depends
only on `dispatch.provider/v1`. The public mock and conformance runner are the
executable example; the mock allocates no real infrastructure.

## Run the sidecar

The [Compose package](../examples/providers/sidecar/compose.yaml) shares the
running controller's network namespace and listens only on its loopback address.
It exposes no additional host port and mounts only its own persistent state
volume. This example targets Linux containers with Docker Compose.

From a checkout of Dispatch:

```sh
export DISPATCH_CONTAINER=$(docker compose -f compose.yml ps -q dispatch)
export DISPATCH_PROVIDER_IMAGE=ghcr.io/doout/dispatch-provider-mock:main
docker compose -f examples/providers/sidecar/compose.yaml up -d
```

For a release, replace `:main` with a published `:sha-<commit>` tag or a verified
`@sha256:<digest>` reference. The provider conformance workflow publishes both
`main` and commit tags after its checks pass. Its first GHCR publication also
requires the package owner to make the image public; the workflow verifies an
anonymous pull. If the image is not yet published, build the identical package:

```sh
docker build -f Containerfile.provider-mock -t dispatch-provider-mock:local .
export DISPATCH_PROVIDER_IMAGE=dispatch-provider-mock:local
docker compose -f examples/providers/sidecar/compose.yaml up -d
```

As controller owner, register `http://127.0.0.1:8091` in **Servers → Infrastructure
providers**, with no authentication and inspect/create/delete capabilities.
The [registration body](../examples/providers/sidecar/registration.json) is also
accepted by `POST /api/v1/infrastructure/providers`. Run the managed-server
workflow in [On-demand servers](on-demand-servers.md); the mock's machine needs
a separately installed agent before it becomes a deployment target.

For authenticated provider packages, inject `DISPATCH_PROVIDER_TOKEN` through
the provider operator's private deployment environment or secret manager before
starting the mock. Save the same value as a write-only Dispatch secret and use
its ID as `credentialSecretId` in registration. Never put its value in the
registration body or checked-in Compose files. A separate adapter can use its
own native secret mount or credential mechanism; keep cloud credentials inside
that adapter. Non-loopback and private-network endpoints require verified HTTPS.

Run conformance from the controller's network namespace:

```sh
docker run --rm --network "container:${DISPATCH_CONTAINER}" \
  --entrypoint /usr/local/bin/dispatch-provider-conformance \
  "$DISPATCH_PROVIDER_IMAGE" --allow-mutations
```

Only run mutation conformance against an isolated test account for a real
provider. Add `-e DISPATCH_PROVIDER_TOKEN` for the authenticated mock.

## Compatibility and upgrade procedure

| Evidence | Supported behavior |
| --- | --- |
| API version | Exactly `dispatch.provider/v1`; unknown versions block mutations and report the supported version |
| Adapter identity | Manifest name and API version remain fixed within a registration |
| Capabilities | Every owner-approved capability must still be advertised |
| Configuration | Bounded object JSON Schema, no external references, undeclared fields rejected |
| Create retry | Exact reviewed manifest and encrypted input remain pinned until submission is acknowledged |
| Owned resources | Current approved v1 inspection/deletion can operate on existing resources after an upgrade, with exact ownership labels |
| Provider persistence | Stable operation, resource and idempotency-key records survive image replacement and rollback |

1. Inspect pending operations and drain create submissions before changing the
   adapter version. Preserve its state volume/database and record the current
   image digest. The provider repository must declare backward-compatible state
   migration and rollback rules; the controller cannot repair a lost adapter ledger.
2. Disable the registration to stop new mutations. Finish or inspect previously
   accepted work before replacing the image.
3. Select the new provider image in the operator's deployment environment and
   run the same Compose command. The endpoint and provider-owned state remain.
4. Verify the registration, review manifest/schema/capability changes, then
   explicitly enable it. Verification cannot replace the adapter name/API
   identity, and old create reviews do not become valid for a new manifest.
5. If verification fails or the service is unavailable, retain the disabled
   registration and restore the recorded image with its compatible persisted
   state. Verify and re-enable. Resource ownership and controller operation
   history remain intact; inspect unknown operations before any further mutation.

An endpoint or route change creates a new registration, as it changes the trust
target. Keep the old registration for its resources and outstanding operations.
Use a stable HTTPS endpoint, service discovery address, or the sidecar namespace
to replace implementations without changing the registered target. Moving
existing resource ownership to another registration requires an explicit future
migration flow; creating a new endpoint does not silently adopt old machines.

## Public examples and CI

`python3 scripts/check-provider-fixtures.py` rejects common credential formats
in the public provider examples, docs, adapter and conformance fixtures. Review
new fixtures for private endpoints, tenant/account IDs, proprietary images and
real credentials before committing them. The scanner is a review aid; it does
not prove that arbitrary text is public.

The provider workflow runs compatibility/upgrade tests against the persistent
mock, builds the image, and executes
`scripts/test-provider-sidecar.sh`. The sidecar test starts isolated containers,
checks conformance, simulates incompatible/unavailable upgrades, and rolls back
with the original volume. It removes only its own test containers and volume.
Controller tests verify that failed upgrades preserve registration and resource
history and that an approved compatible upgrade still permits owned deletion.
