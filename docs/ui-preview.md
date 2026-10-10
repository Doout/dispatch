# UI development with production data

The UI preview proxy serves the frontend from your development checkout. Its **Data source** selector switches API requests between a development backend and a production backend. Development is the default.

The preview image includes the public landing page while the production image
opens at sign-in. To hide it in a preview build too, use
`--build-arg VITE_HOSTED_LANDING_PAGE=false`.

Production mode requires a separate preview access key before your production login. The proxy reads this key from a private file and issues a signed, HttpOnly cookie valid for one hour. A user login or a source cookie alone cannot enable production access. Access expiry also closes active log streams.

Production mode uses your production permissions. The proxy blocks application changes, including tenant creation, invitations, password changes and deployment actions. It also blocks connection setup callbacks and worker job leases, which use GET requests to change state. Login, logout and tenant sign-in handoffs still work. Data reads and live log streams pass through. Production sessions have separate cookies, so switching back restores your development session.

The proxy has no database connection, migrations, controllers or workers. It does not copy production data or store a production password. Production API responses and the preview HTML are never cached. Regular Dispatch installations do not show the selector.

## Run in Docker

Generate a separate key for this preview installation and give the container user read access. Keep the file outside the repository.

```sh
openssl rand -hex 32 > ui-preview-access-key
sudo chown 65532:65532 ui-preview-access-key
sudo chmod 600 ui-preview-access-key
```

Build an image when the frontend changes:

```sh
docker build -f Containerfile.ui-preview -t dispatch-ui-preview:dev .
```

Add this service to your development control plane's Compose file. This example shares the gateway's network namespace with the development backend, which listens on port 8081. The proxy listens on port 8082.

```yaml
services:
  ui-preview:
    image: dispatch-ui-preview:dev
    restart: unless-stopped
    read_only: true
    cap_drop: [ALL]
    security_opt: [no-new-privileges:true]
    network_mode: service:gateway
    depends_on:
      gateway:
        condition: service_started
    environment:
      DISPATCH_UI_PREVIEW_ADDR: 127.0.0.1:8082
      DISPATCH_UI_PREVIEW_ORIGIN: https://dispatch.local.cicd.onl
      DISPATCH_UI_PREVIEW_DEVELOPMENT: http://127.0.0.1:8081
      DISPATCH_UI_PREVIEW_PRODUCTION: https://dispatch.cicd.onl
      DISPATCH_UI_PREVIEW_ACCESS_KEY_FILE: /run/secrets/ui-preview-access-key
    volumes:
      - ./ui-preview-access-key:/run/secrets/ui-preview-access-key:ro
    logging:
      options:
        max-size: 10m
        max-file: '3'
```

Do not mount the backend's database or provider credentials in this container. Both browser-facing origins must use HTTPS. The development backend may use HTTP on loopback. All origins must omit paths, queries and credentials.

Start only the new service:

```sh
docker compose up -d --no-deps ui-preview
```

Change the development gateway's upstream from `http://127.0.0.1:8081` to `http://127.0.0.1:8082`, validate its configuration and reload it. Keep the gateway's buffering disabled for live logs. A gateway reload preserves the network namespace used by the backend.

Open the dev URL and select **Production**. Enter the preview access key, then sign in with your production account. The UI uses the production API while retaining the frontend built into the preview image. API changes must remain compatible with the production backend.

## Lock and rotate access

The **Lock** button clears the browser's unlock cookie and returns to Development. Switching sources alone keeps the unlock until it expires. The key is never sent to the production backend, embedded in HTML or saved in browser storage.

To revoke all outstanding unlock cookies, replace the private key file and recreate only the proxy:

```sh
docker compose up -d --no-deps --force-recreate ui-preview
```

A missing, unreadable or short key prevents startup. The proxy has no keyless mode. It validates the signed unlock on every production request; changing the source cookie cannot bypass it.

## Tenant navigation

The proxy maps production tenant console links and sign-in redirects to the same tenant under the development domain. It preserves sign-in handoff codes and their PKCE checks. The tenant directory still displays production addresses.

For tenant navigation, the development gateway, DNS and certificate must cover `*.dispatch.local.cicd.onl`. Without that coverage, the root account console works, but tenant consoles cannot open through dev. Links to deployed applications keep their actual destinations.

## Return to the regular dev backend

Point the gateway back at port 8081, validate and reload its configuration, then stop the `ui-preview` service. The development backend and database do not need to restart.
