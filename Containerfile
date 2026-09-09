FROM node:22.13-alpine AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN npm install --global pnpm@11.18.0 && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS build
ARG DISPATCH_VERSION=development
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/ui/dist ./internal/ui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/doout/dispatch/internal/installation.Version=${DISPATCH_VERSION}" -o /out/dispatch ./cmd/dispatch && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/edge/linux-amd64 ./cmd/dispatch-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/edge/linux-arm64 ./cmd/dispatch-agent && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dispatch-hook ./cmd/dispatch-hook && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/relay/linux-amd64 ./cmd/dispatch-relay && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /out/relay/linux-arm64 ./cmd/dispatch-relay

FROM docker:cli
RUN apk add --no-cache bash ca-certificates git openssh-client poetry && \
    addgroup -S -g 65532 dispatch && \
    adduser -S -D -H -u 65532 -G dispatch dispatch
COPY --from=build /out/dispatch /usr/local/bin/dispatch
COPY --from=build /out/dispatch-hook /usr/local/bin/dispatch-hook
COPY --from=build /out/relay /usr/local/lib/dispatch-relay
COPY --from=build /out/edge /usr/local/lib/dispatch-edge
USER dispatch:dispatch
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dispatch"]
