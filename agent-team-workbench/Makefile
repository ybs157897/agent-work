.PHONY: build test test-race vet fmt migrate knowledge-client run-control-plane run-runnerd code-workspace-build code-workspace-jdtls

build:
	go build ./...

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

migrate:
	go run ./cmd/migrate

# 前端（web/）：开发走 Vite 代理；产物由 control-plane 静态托管（WEB_DIST，默认 web/dist）。
web-install:
	cd web && pnpm install

web-dev:
	cd web && pnpm dev

web-build:
	cd web && pnpm build

# The complete Java reading subsystem is built from the recorded source snapshot.
code-workspace-build:
	cd ../integrations/web-idea/apps/gateway && go build -o bin/web-idea-gateway .
	cd ../integrations/web-idea/apps/web && pnpm install --frozen-lockfile && pnpm build

code-workspace-jdtls:
	bash ../integrations/web-idea/apps/jdtls-sidecar/scripts/fetch-jdtls.sh

knowledge-client:
	mkdir -p bin
	go build -o bin/atw-knowledge ./cmd/atw-knowledge

run-control-plane: knowledge-client
	go run ./cmd/control-plane

run-runnerd:
	go run ./cmd/runnerd
