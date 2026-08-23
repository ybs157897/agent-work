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
            send({"id": rid, "result": {"turn": {
                "id": "turn_fake_1", "status": "inProgress", "items": [], "error": None}}})
            send({"method": "turn/started", "params": {
                "threadId": thread_id,
                "turn": {"id": "turn_fake_1", "status": "inProgress", "items": []}}})
            if os.environ.get("CODEX_FAKE_HANG") == "1":
                continue

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
                "cwd": "/tmp", "status": "completed"}}})
            send({"method": "item/agentMessage/delta",
                  "params": {"threadId": thread_id, "turnId": "turn_fake_1",
                             "itemId": "msg_1", "delta": "fake codex 输出"}})

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
