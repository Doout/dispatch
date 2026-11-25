.PHONY: build check clean dev web doodship

build: web doodship
	go build -trimpath -o dispatch ./cmd/dispatch
	go build -trimpath -o dispatchctl ./cmd/dispatchctl
	go build -trimpath -o dispatch-agent ./cmd/dispatch-agent
	go build -trimpath -o dispatch-relay ./cmd/dispatch-relay

doodship:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o doodship ./cmd/doodship

web:
	corepack pnpm@11.18.0 --dir web install --frozen-lockfile
	corepack pnpm@11.18.0 --dir web build

check:
	go test -race -timeout 40m ./...
	go vet ./...
	corepack pnpm@11.18.0 --dir web typecheck
	corepack pnpm@11.18.0 --dir web test --maxWorkers=2

dev: web
	DISPATCH_DEMO=true go run ./cmd/dispatch

clean:
	go clean
	rm -f dispatch dispatch-agent dispatch-relay dispatchctl doodship
