FROM --platform=$BUILDPLATFORM node:22.13-alpine AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN npm install --global pnpm@11.18.0 && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG DISPATCH_VERSION=development
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY --from=web /src/internal/ui/dist ./internal/ui/dist
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w -X github.com/doout/dispatch/internal/installation.Version=${DISPATCH_VERSION}" -o /out/dispatch ./cmd/dispatch && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/edge/linux-amd64 ./cmd/dispatch-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/edge/linux-arm64 ./cmd/dispatch-agent && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /out/dispatch-hook ./cmd/dispatch-hook && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/relay/linux-amd64 ./cmd/dispatch-relay && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/relay/linux-arm64 ./cmd/dispatch-relay

FROM docker:cli AS runtime
RUN apk add --no-cache bash ca-certificates git openssh-client poetry && \
    addgroup -S -g 65532 dispatch && \
    adduser -S -D -H -u 65532 -G dispatch dispatch
USER dispatch:dispatch
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dispatch"]

# CI packages the same cached binaries used by the downloadable archives.
FROM runtime AS release
ARG TARGETARCH
COPY release/${TARGETARCH}/dispatch /usr/local/bin/dispatch
COPY release/${TARGETARCH}/dispatch-hook /usr/local/bin/dispatch-hook
COPY release/relay /usr/local/lib/dispatch-relay
COPY release/edge /usr/local/lib/dispatch-edge

# Default target keeps local source builds self-contained.
FROM runtime AS controller
COPY --from=build /out/dispatch /usr/local/bin/dispatch
COPY --from=build /out/dispatch-hook /usr/local/bin/dispatch-hook
COPY --from=build /out/relay /usr/local/lib/dispatch-relay
COPY --from=build /out/edge /usr/local/lib/dispatch-edge
