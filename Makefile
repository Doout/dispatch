.PHONY: build check clean dev web

build: web
	go build -trimpath -o dispatch ./cmd/dispatch
	go build -trimpath -o dispatch-agent ./cmd/dispatch-agent

web:
	corepack pnpm --dir web install --frozen-lockfile
	corepack pnpm --dir web build

check:
	go test -race ./...
	go vet ./...
	corepack pnpm --dir web typecheck
	corepack pnpm --dir web test

dev: web
	DISPATCH_DEMO=true go run ./cmd/dispatch

clean:
	go clean
	rm -f dispatch dispatch-agent
