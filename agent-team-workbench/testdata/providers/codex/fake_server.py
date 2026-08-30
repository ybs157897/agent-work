#!/usr/bin/env python3
"""Strict fake for the Codex app-server v2 subset used by Workbench."""

import json
import os
import sys


def send(frame):
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def rpc_error(request_id, message, code=-32000):
    send({"id": request_id, "error": {"code": code, "message": message}})


def complete_turn(thread_id, status="completed", error=None):
    turn = {"id": "turn_fake_1", "status": status}
    if error:
        turn["error"] = {"message": error}
    send({"method": "turn/completed", "params": {"threadId": thread_id, "turn": turn}})


def main():
    thread_id = "th_fake_1"
    initialize_responded = False
    initialized = False

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue

        rid = req.get("id")
        method = req.get("method")
        params = req.get("params") or {}

        if method == "initialize":
            capabilities = params.get("capabilities") or {}
            if not params.get("clientInfo", {}).get("name"):
                rpc_error(rid, "missing clientInfo.name")
                continue
            if not capabilities.get("experimentalApi"):
                rpc_error(rid, "experimentalApi required by fixture")
                continue
            initialize_responded = True
            send({"id": rid, "result": {
                "userAgent": "codex-cli/0.149.0-fake", "codexHome": "/tmp",
                "platformFamily": "unix", "platformOs": "fake"}})
            continue

        if method == "initialized" and rid is None:
            if initialize_responded:
                initialized = True
            continue

        if not initialized:
            if rid is not None:
                rpc_error(rid, "Not initialized", -32002)
            continue

        if method == "account/read":
            if os.environ.get("CODEX_FAKE_NO_AUTH") == "1":
                send({"id": rid, "result": {"account": None, "requiresOpenaiAuth": True}})
            else:
                send({"id": rid, "result": {
                    "account": {"type": "chatgpt", "planType": "pro"},
                    "requiresOpenaiAuth": True}})
        elif method == "model/list":
            send({"id": rid, "result": {"data": [{
                "id": "gpt-fake", "model": "gpt-fake", "displayName": "GPT Fake",
                "isDefault": True, "hidden": False,
                "supportedReasoningEfforts": [{"reasoningEffort": "high"}],
            }], "nextCursor": None}})
        elif method == "thread/start":
            if os.environ.get("CODEX_EXPECT_RESUME") == "1":
                rpc_error(rid, "expected thread/resume")
                continue
            send({"id": rid, "result": {
                "thread": {"id": thread_id, "sessionId": thread_id},
                "model": "gpt-fake", "modelProvider": "openai", "cwd": "/tmp"}})
            send({"method": "thread/started", "params": {"thread": {"id": thread_id}}})
        elif method == "thread/resume":
            if os.environ.get("CODEX_FAKE_RESUME_NOT_FOUND") == "1":
                rpc_error(rid, "Thread not found: {}".format(params.get("threadId")))
                continue
            thread_id = params.get("threadId") or thread_id
            send({"id": rid, "result": {
                "thread": {"id": thread_id, "sessionId": thread_id},
                "model": "gpt-fake", "modelProvider": "openai", "cwd": "/tmp"}})
        elif method == "turn/start":
            expected_effort = os.environ.get("CODEX_EXPECT_EFFORT")
            if expected_effort and params.get("effort") != expected_effort:
                rpc_error(rid, "unexpected effort: {}".format(params.get("effort")))
                continue
            if "multiAgentMode" in params:
                rpc_error(rid, "deprecated multiAgentMode must not be sent")
                continue
            send({"id": rid, "result": {"turn": {
                "id": "turn_fake_1", "status": "inProgress", "items": [], "error": None}}})
            send({"method": "turn/started", "params": {
                "threadId": thread_id,
                "turn": {"id": "turn_fake_1", "status": "inProgress", "items": []}}})
            if os.environ.get("CODEX_FAKE_HANG") == "1":
                continue

            if os.environ.get("CODEX_FAKE_CHILD") == "1":
                send({"method": "item/started", "params": {"item": {
                    "id": "collab_1", "type": "collabAgentToolCall", "tool": "spawn_agent",
                    "status": "inProgress", "receiverThreadIds": ["th_child_1"],
                    "agentsStates": {"th_child_1": {"status": "running"}},
                    "prompt": "inspect fixture"}}})

                # Codex app-server multiplexes parent and child notifications on
                # the same stdout stream. Reuse the root turn id deliberately:
                # threadId, rather than turnId, is the agent-scope authority.
                if os.environ.get("CODEX_FAKE_LIVE_CHILD") == "1":
                    child_id = "th_child_1"
                    send({"method": "turn/started", "params": {
                        "threadId": child_id,
                        "turn": {"id": "turn_fake_1", "status": "inProgress", "items": []}}})
                    send({"method": "item/started", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "item": {"id": "live_child_reason", "type": "reasoning", "status": "inProgress"}}})
                    send({"method": "item/reasoning/summaryTextDelta", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "itemId": "live_child_reason", "delta": "child live thinking"}})
                    send({"method": "item/started", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "item": {"id": "live_child_tool", "type": "commandExecution",
                                 "command": "echo child", "cwd": "/tmp", "status": "inProgress"}}})
                    send({"method": "item/commandExecution/outputDelta", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "itemId": "live_child_tool", "delta": "child output\n"}})
                    send({"method": "item/completed", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "item": {"id": "live_child_tool", "type": "commandExecution",
                                 "command": "echo child", "cwd": "/tmp", "status": "completed",
                                 "aggregatedOutput": "child output\n", "exitCode": 0}}})
                    send({"method": "item/started", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "item": {"id": "live_child_msg", "type": "agentMessage", "status": "inProgress"}}})
                    send({"method": "item/agentMessage/delta", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "itemId": "live_child_msg", "delta": "child live final"}})
                    send({"method": "item/completed", "params": {
                        "threadId": child_id, "turnId": "turn_fake_1",
                        "item": {"id": "live_child_msg", "type": "agentMessage",
                                 "status": "completed", "text": "child live final"}}})
                    send({"method": "turn/completed", "params": {
                        "threadId": child_id,
                        "turn": {"id": "turn_fake_1", "status": "completed"}}})

            send({"method": "item/reasoning/summaryTextDelta",
                  "params": {"threadId": thread_id, "turnId": "turn_fake_1",
                             "itemId": "reason_1", "delta": "fake reasoning"}})
            send({"method": "item/started", "params": {"item": {
                "id": "it_1", "type": "commandExecution", "command": "echo hi",
                "cwd": "/tmp", "status": "inProgress"}}})
            send({"method": "item/commandExecution/outputDelta",
                  "params": {"threadId": thread_id, "turnId": "turn_fake_1",
                             "itemId": "it_1", "delta": "hi\n"}})
            send({"method": "item/completed", "params": {"item": {
                "id": "it_1", "type": "commandExecution", "command": "echo hi",
                "cwd": "/tmp", "status": "completed",
                "aggregatedOutput": "hi\n", "exitCode": 0}}})
            # 三类通知帧的回放覆盖（泵级脚本化直测见 codexapp_test.go 的
            # TestPump*；conformance 端到端路径依赖这里发帧）。
            # turn/plan/updated 两帧：每帧携带全量清单（末帧行集为终态，替换非追加）。
            send({"method": "turn/plan/updated", "params": {
                "threadId": thread_id, "turnId": "turn_fake_1", "explanation": "fake plan",
                "plan": [{"step": "调研现有实现", "status": "inProgress"},
                         {"step": "补回放桩帧", "status": "pending"}]}})
            send({"method": "turn/plan/updated", "params": {
                "threadId": thread_id, "turnId": "turn_fake_1",
                "plan": [{"step": "调研现有实现", "status": "completed"},
                         {"step": "补回放桩帧", "status": "completed"}]}})
            # contextCompaction 主路径：started 只表压缩进行中（不得发事件），
            # completed 才落 session.compacted。
            send({"method": "item/started", "params": {
                "threadId": thread_id, "turnId": "turn_fake_1",
                "item": {"id": "it_compact_1", "type": "contextCompaction"}}})
            send({"method": "item/completed", "params": {
                "threadId": thread_id, "turnId": "turn_fake_1", "completedAtMs": 1756000000000,
                "item": {"id": "it_compact_1", "type": "contextCompaction"}}})
            # token 用量：per_run 增量只取 last（total 是 thread 累计，不参与）；
            # turn_prev 帧演练归因过滤——异 turn 增量不得计入本轮累计。
            send({"method": "thread/tokenUsage/updated", "params": {
                "threadId": thread_id, "turnId": "turn_prev",
                "tokenUsage": {"total": {"inputTokens": 9000, "cachedInputTokens": 8000, "outputTokens": 7000},
                               "last": {"inputTokens": 3000, "cachedInputTokens": 2000, "outputTokens": 1000}}}})
            send({"method": "thread/tokenUsage/updated", "params": {
                "threadId": thread_id, "turnId": "turn_fake_1",
                "tokenUsage": {"total": {"inputTokens": 120, "cachedInputTokens": 80, "outputTokens": 40},
                               "last": {"inputTokens": 120, "cachedInputTokens": 80, "outputTokens": 40}}}})
            send({"method": "thread/tokenUsage/updated", "params": {
                "threadId": thread_id, "turnId": "turn_fake_1",
                "tokenUsage": {"total": {"inputTokens": 350, "cachedInputTokens": 230, "outputTokens": 100},
                               "last": {"inputTokens": 230, "cachedInputTokens": 150, "outputTokens": 60}}}})
            send({"method": "item/agentMessage/delta",
                  "params": {"threadId": thread_id, "turnId": "turn_fake_1",
                             "itemId": "msg_1", "delta": "fake codex 输出"}})

            if os.environ.get("CODEX_FAKE_CHILD") == "1":
                send({"method": "item/completed", "params": {"item": {
                    "id": "collab_1", "type": "collabAgentToolCall", "tool": "spawn_agent",
                    "status": "completed", "receiverThreadIds": ["th_child_1"],
                    "agentsStates": {"th_child_1": {"status": "completed"}},
                    "prompt": "inspect fixture"}}})

            if os.environ.get("CODEX_FAKE_APPROVAL") == "1":
                send({"id": 500, "method": "item/commandExecution/requestApproval",
                      "params": {"threadId": thread_id, "turnId": "turn_fake_1",
                                 "itemId": "it_approval", "startedAtMs": 1,
                                 "command": "echo high-risk", "cwd": "/tmp"}})
                response = json.loads(sys.stdin.readline())
                decision = response.get("result", {}).get("decision")
                expected = "accept" if os.environ.get("CODEX_EXPECT_APPROVED") == "1" else "cancel"
                if decision != expected:
                    rpc_error(rid, "invalid approval response")
                    continue
                if decision == "cancel":
                    complete_turn(thread_id, "interrupted")
                    continue

            send({"method": "item/completed", "params": {"item": {
                "id": "msg_1", "type": "agentMessage", "text": "fake codex 输出"}}})
            if os.environ.get("CODEX_FAKE_FAIL") == "turn":
                complete_turn(thread_id, "failed", "fixture turn failure")
            else:
                complete_turn(thread_id)
        elif method == "turn/steer":
            if params.get("threadId") != thread_id or params.get("expectedTurnId") != "turn_fake_1":
                rpc_error(rid, "invalid steer precondition")
                continue
            send({"id": rid, "result": {"turnId": "turn_fake_1"}})
            send({"method": "item/agentMessage/delta",
                  "params": {"threadId": thread_id, "turnId": "turn_fake_1",
                             "itemId": "msg_steer", "delta": "steered"}})
            send({"method": "item/completed", "params": {"item": {
                "id": "msg_steer", "type": "agentMessage", "text": "steered"}}})
            complete_turn(thread_id)
        elif method == "thread/turns/list":
            if params.get("threadId") != "th_child_1":
                rpc_error(rid, "unknown child thread")
                continue
            if params.get("cursor"):
                send({"id": rid, "result": {"data": [{"id": "child_turn_1", "status": "completed", "items": [
                    {"id": "child_reason_1", "type": "reasoning", "status": "completed", "summary": ["child thinking"], "content": []},
                    {"id": "child_msg_1", "type": "agentMessage", "status": "completed", "text": "child final"}
                ]}], "nextCursor": None}})
            else:
                send({"id": rid, "result": {"data": [{"id": "child_turn_1", "status": "completed", "items": [
                    {"id": "child_reason_1", "type": "reasoning", "status": "completed", "summary": ["child thinking"], "content": []},
                    {"id": "child_tool_1", "type": "commandExecution", "status": "completed", "command": "echo child", "aggregatedOutput": "child output", "exitCode": 0}
                ]}], "nextCursor": "page-2"}})
        elif method == "thread/list":
            send({"id": rid, "result": {"data": [{"id": "th_child_1", "agentNickname": "调查员", "agentRole": "coder", "preview": "检查 fixture", "parentThreadId": "th_fake_1", "createdAt": 4102444800}]}})
        elif method == "turn/interrupt":
            if not params.get("turnId"):
                rpc_error(rid, "turnId required")
                continue
            send({"id": rid, "result": {}})
            complete_turn(thread_id, "interrupted")
        elif rid is not None:
            rpc_error(rid, "method not found", -32601)
    return 0


if __name__ == "__main__":
    sys.exit(main())
