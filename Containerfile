FROM node:22.13-alpine AS web
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN npm install --global pnpm@11.18.0 && pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/ui/dist ./internal/ui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dispatch ./cmd/dispatch && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/dispatch-agent ./cmd/dispatch-agent

FROM docker:cli
RUN apk add --no-cache ca-certificates git && \
    addgroup -S -g 65532 dispatch && \
    adduser -S -D -H -u 65532 -G dispatch dispatch
COPY --from=build /out/dispatch /usr/local/bin/dispatch
USER dispatch:dispatch
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/dispatch"]
