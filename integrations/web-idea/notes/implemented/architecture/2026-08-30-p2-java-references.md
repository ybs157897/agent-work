# P2 Java references via jdtls

Date: 2026-08-30

Status: implemented (MVP)

## Decision

- Browser never talks to jdtls directly; Gateway `GET /api/v1/workspaces/{id}/lsp` upgrades to WebSocket and proxies JSON-RPC with URI rewrite (`webidea://ws/{id}/…` ↔ `file://`).
- Web: Cmd/Ctrl+click on a Java identifier opens a Find References peek; click a hit to open file at line.
- jdtls is optional: unset `WEBIDEA_JDTLS_LAUNCH` → browse-only; peek shows unavailable.

## Why

Matches architecture: Gateway trust boundary + Java semantics only via jdtls; P2 exit is project-source references, not full IDE nav.

## Follow-ups

- definition / hover
- wait for jdtls workspace/initialized before first references
- P3: open `jdt://` decompiled
