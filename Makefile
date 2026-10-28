.PHONY: generate lint test test-app e2e e2e-real e2e-chaos build app release clean

generate:
	go tool buf generate
	go tool buf generate --template buf.gen.swift.yaml
	pnpm install --frozen-lockfile
	go tool buf generate --template buf.gen.ts.yaml

lint:
	go tool buf lint
	go vet ./...
	test -z "$$(gofmt -l cmd internal pkg e2e)"

test:
	go test ./cmd/... ./internal/... ./pkg/... -count=1

# The desktop app's pure logic and types, the shared Swift package, and the macOS signer.
test-app:
	pnpm install --frozen-lockfile
	pnpm -r typecheck
	pnpm --filter @hermesh/core test
	pnpm --filter @hermesh/desktop test
	cd packages/HermeshKit && swift test
	cd apps/signer/macos && swift test

# End-to-end with real binaries and scripted agents.
e2e:
	go test ./e2e/ -count=1 -timeout 30m

# End-to-end with this machine's real Claude Code and Codex (costs tokens).
e2e-real:
	HERMESH_REAL_CLI=1 go test ./e2e/ ./internal/provider/providertest/ -count=1 -v -timeout 30m

# Real Codex under injected faults: killed processes, relay restart,
# provider outage, missing login, concurrency, agent git rewrites.
e2e-chaos:
	HERMESH_CHAOS=1 go test ./e2e/ -run TestChaos -count=1 -v -timeout 90m

build:
	mkdir -p bin
	go build -o bin/ ./cmd/...

app:
	pnpm install --frozen-lockfile
	node apps/desktop/scripts/build-app.mjs

release:
	./scripts/release.sh

clean:
	rm -rf bin dist apps/desktop/out apps/signer/macos/.build packages/HermeshKit/.build apps/ios/build
