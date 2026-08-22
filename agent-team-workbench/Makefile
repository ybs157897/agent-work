.PHONY: build test test-race vet fmt migrate run-control-plane run-runnerd

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

run-control-plane:
	go run ./cmd/control-plane

run-runnerd:
	go run ./cmd/runnerd
