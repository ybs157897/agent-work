# Codex app-server Adapter Contract

Status: implemented

Adapter id: `codex-appserver`  
Runtime label: `codex_local`  
Transport: child-process stdio, newline-delimited JSON  
Protocol: bidirectional JSON-RPC 2.0 with the `jsonrpc` field omitted  
Baseline: Codex CLI `0.149.0`, experimental v2 schema SHA-256 `6f76cce25156d405f1da54f205751e38f7b9eb42246ac0742b9958dd60275350`

The official protocol reference is <https://developers.openai.com/codex/app-server>. The executable remains the source of truth for the installed version:

```sh
codex app-server generate-json-schema --experimental --out ./schemas
```

## Connection and run lifecycle

Every Workbench Run owns one local app-server process. A provider thread is durable and can span multiple Workbench Runs.

1. Client sends `initialize` with `clientInfo` and `capabilities.experimentalApi=true`.
2. After the response, client sends the `initialized` notification.
3. New conversation: `thread/start`; continued conversation: `thread/resume` with the previously persisted thread id.
4. The returned `thread.id` is stored as the private `codex://<thread-id>` Run session reference.
5. Client sends `turn/start`; the response or `turn/started` supplies the active turn id.
6. Notifications are projected into canonical Workbench message/tool/status events.
7. `turn/completed` is terminal authority. The process is terminated and reaped after the terminal projection is committed.

If Agent prompt, model, mode, sandbox, approval policy, or Runtime changes, the orchestrator does not resume the old thread. It starts a new thread and injects the bounded canonical conversation history into the first user turn.

## Configuration mapping

| Workbench snapshot | Codex app-server field |
|---|---|
| `model.model` | `thread/start.model`, `thread/resume.model` |
| Agent `instructions` | `developerInstructions` |
| `policy.approval_policy=auto` | `approvalPolicy=never` |
| `policy.approval_policy=approve_high_risk` | `approvalPolicy=on-request` |
| `policy.approval_policy=manual` | `approvalPolicy=untrusted` |
| `policy.sandbox` | `sandbox` (`read-only`, `workspace-write`, `danger-full-access`) |
| `mode=plan` | experimental `turn/start.collaborationMode` |

An empty Codex model means “use the current Codex CLI default”. Non-Codex provider models are rejected before dispatch.

## Event projection

| App-server method/item | Canonical Workbench event |
|---|---|
| `turn/started` | Run `running` |
| `item/agentMessage/delta` | `message.delta` (`text-delta`) |
| `item/plan/delta` | `message.delta` (`text-delta`) |
| `item/reasoning/*Delta` | `message.delta` (`reasoning-delta`) |
| completed `agentMessage` / `plan` item | `message.completed` |
| started command/file/MCP/dynamic/web item | `tool.started` |
| completed tool item | `tool.completed` or `tool.failed` |
| `item/commandExecution/outputDelta` | `tool.progress` |
| `turn/completed.status=completed` | `succeeding` then `succeeded` |
| `turn/completed.status=interrupted` | `interrupted` or `cancelled` according to the control command |
| `turn/completed.status=failed` | `failed` with the provider error message |

`item/completed` is authoritative for final item state; deltas are display-only and never determine terminal success.

## Interactive methods

- Steering: `turn/steer` with exact `threadId`, `expectedTurnId`, and text input.
- Interrupt: `turn/interrupt` with exact `threadId` and `turnId`.
- Command approval response: `{ "decision": "accept" | "cancel" }`.
- File-change approval response: `{ "decision": "accept" | "cancel" }`.
- Permission approval response: the granted subset in `{ "permissions": ..., "scope": "turn" }`; rejection grants an empty subset and interrupts the turn.

Approval routing is keyed by `Run id + Workbench approval id`. One UI decision cannot release another pending provider request.

## Probe contract

Probe starts a disposable app-server process and verifies:

1. `initialize` response and `initialized` notification;
2. `account/read` reports a usable auth state;
3. `model/list` returns at least one visible model.

Binary existence alone is not considered healthy. Probe never starts a thread or consumes model tokens.

## Explicit limitations

- The adapter uses local stdio. Remote WebSocket app-server transport is not exposed by this binding.
- Workbench tool allowlists are rejected because the stable built-in-tool restriction surface is not equivalent to the Workbench whitelist. Sandbox and approval policies remain enforced.
- MCP elicitation and `item/tool/requestUserInput` are not yet represented in the Workbench interaction domain; unexpected server requests receive JSON-RPC `-32601` rather than being silently accepted.
