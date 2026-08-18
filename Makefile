.PHONY: build check clean dev web

build: web
	go build -trimpath -o dispatch ./cmd/dispatch
	go build -trimpath -o dispatch-agent ./cmd/dispatch-agent
	go build -trimpath -o dispatch-relay ./cmd/dispatch-relay

web:
	corepack pnpm@11.18.0 --dir web install --frozen-lockfile
	corepack pnpm@11.18.0 --dir web build

check:
	go test -race ./...
	go vet ./...
	corepack pnpm@11.18.0 --dir web typecheck
	corepack pnpm@11.18.0 --dir web test

dev: web
	DISPATCH_DEMO=true go run ./cmd/dispatch

clean:
	go clean
	rm -f dispatch dispatch-agent dispatch-relay
