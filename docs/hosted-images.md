# Hosted control plane images

The `Hosted images` workflow publishes `ghcr.io/doout/dispatch-platform` after CI
succeeds for a push to `main`. It builds Linux amd64 and arm64 images from the
exact tested commit using `Containerfile.hosted`.

| Reference | Use |
| --- | --- |
| `ghcr.io/doout/dispatch-platform:main` | Follow the current main build after CI and image checks pass. |
| `ghcr.io/doout/dispatch-platform:sha-<full-commit-sha>` | Select a particular published commit. Reruns reuse this image. |
| `ghcr.io/doout/dispatch-platform@sha256:<digest>` | Pin the exact image manifest for deployment or rollback. |

Each publication records the commit, image tag and digest in its Actions summary.
The image's `version` command reports `main-<full-commit-sha>`.

The workflow checks that the commit is still the head of `main` before building
and before updating the `main` tag. An older build finishing later cannot replace
a newer published build. Pull request runs cannot publish images. Publication
does not update running servers.

## Pulling updates

Set the control plane's Compose image to
`ghcr.io/doout/dispatch-platform:main`. To update a service named `platform`, run
these commands from its Compose directory:

```sh
docker compose pull platform
docker compose up -d --no-deps --no-build platform
```

Record the running image digest before an update. For a repeatable deployment or
rollback, set the Compose image to the digest from the Actions summary instead
of `main`. Back up PostgreSQL and the private state directory before updating.
A previous image alone cannot undo a database migration.

## Second installation at x.dispatch.cicd.onl

Prepare the second server using [Hosted development instance](hosted-dev.md).
Use `x.dispatch.cicd.onl` as its root domain and give it its own database volume,
state directory, credentials and DNS configuration. In that installation's
`.env`, set:

```dotenv
DISPATCH_HOSTED_ROOT_DOMAIN=x.dispatch.cicd.onl
DISPATCH_PLATFORM_IMAGE=ghcr.io/doout/dispatch-platform:main
```

After the initial setup, run updates from its Compose directory on that server:

```sh
cd /srv/dispatch-platform
docker compose pull platform
docker compose up -d --no-deps --no-build platform
curl --fail --silent --show-error https://x.dispatch.cicd.onl/readyz
docker compose exec platform dispatch-platform version
```

The version output identifies the main commit now running. If the readiness
check fails, inspect `docker compose logs --tail=100 platform` before retrying.
These commands retain the installation's configuration and data volumes. They
must run on the second server; publishing an image does not schedule its update.

## Registry permissions

Publication uses the repository's `GITHUB_TOKEN` with `packages: write`. The GHCR
package must grant this repository Actions access. If a package with this name
already exists under another repository, grant access in its package settings.

To allow servers to pull without credentials, make the `dispatch-platform`
package public in GitHub after its first publication. For a private package,
authenticate Docker on each server with a token that can read the package:

```sh
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io --username YOUR_GITHUB_USERNAME --password-stdin
```

Keep that token out of Compose files and Git. No registry credentials are passed
into the control plane container.

If publication fails, rerun `Hosted images` after fixing the error. It checks CI
and the current branch again and preserves any image already published for that
commit. If `main` has advanced, use the publication associated with its latest
successful CI run.

Tagged hosted releases can be added later. This channel does not change the
existing `ghcr.io/doout/dispatch` release image.
