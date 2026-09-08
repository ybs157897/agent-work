#!/usr/bin/env bash
# 验证文档、Gateway、Web 类型与导航回归
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
required=(
  README.md
  AGENTS.md
  docs/product/vision.md
  docs/architecture/overview.md
  docs/architecture/system-context.md
  docs/architecture/components.md
  docs/architecture/lsp-bridge.md
  docs/architecture/workspace-fs.md
  docs/architecture/security.md
  docs/architecture/deployment.md
  docs/architecture/roadmap.md
  docs/decisions/README.md
  contracts/openapi/gateway-v1.yaml
  contracts/lsp.md
)
for f in "${required[@]}"; do
  test -f "$root/$f" || { echo "missing: $f"; exit 1; }
done
(cd "$root/apps/gateway" && go test ./...)
(cd "$root/apps/web" && pnpm exec tsc -b --pretty false && pnpm test)
echo "ok: docs present + gateway tests + web tsc and navigation tests pass"
