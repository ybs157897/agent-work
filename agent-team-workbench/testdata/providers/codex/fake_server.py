#!/usr/bin/env python3
"""Fake Codex app-server：省略 jsonrpc 字段的 JSON-RPC 回放桩。

覆盖：initialize → thread/start → turn/start → turn/started → item 事件
→ turn/completed；CODEX_FAKE_APPROVAL=1 时插入 requestApproval 服务端请求；
CODEX_FAKE_FAIL=turn 时 turn/completed status=failed。
"""
import json
import os
import sys


def send(frame):
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def main():
    thread_id = "th_fake_1"
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

        if method == "initialize":
            send({"id": rid, "result": {
                "userAgent": "fake-codex/0.149.0", "codexHome": "/tmp",
                "platformFamily": "unix", "platformOs": "fake"}})
        elif method == "thread/start":
            send({"id": rid, "result": {
                "thread": {"id": thread_id}, "model": "fake-model",
                "modelProvider": "fake", "serviceTier": None, "cwd": "/tmp"}})
        elif method == "turn/start":
            send({"id": rid, "result": {"turn": {"status": "inProgress"}}})
            send({"method": "turn/started",
                  "params": {"threadId": thread_id, "turn": {"status": "inProgress"}}})
            send({"method": "item/started",
                  "params": {"item": {"id": "it_1", "type": "commandExecution"}}})
            send({"method": "item/completed",
                  "params": {"item": {"id": "it_1", "type": "commandExecution"}}})

            if os.environ.get("CODEX_FAKE_APPROVAL") == "1":
                send({"id": 500, "method": "item/commandExecution/requestApproval",
                      "params": {"conversationId": thread_id,
                                 "command": "echo high-risk"}})
                # 等待审批响应（下一行输入）后继续。
                resp_line = sys.stdin.readline()
                try:
                    resp = json.loads(resp_line)
                    decision = resp.get("result", {}).get("decision")
                    denied = isinstance(decision, dict) and "denied" in decision
                except json.JSONDecodeError:
                    denied = True
                if denied:
                    send({"method": "turn/completed",
                          "params": {"threadId": thread_id,
                                     "turn": {"status": "interrupted"}}})
                    continue

            if os.environ.get("CODEX_FAKE_FAIL") == "turn":
                send({"method": "turn/completed",
                      "params": {"threadId": thread_id,
                                 "turn": {"status": "failed"}}})
                continue
            send({"method": "turn/completed",
                  "params": {"threadId": thread_id,
                             "turn": {"status": "completed"}}})
        elif method == "turn/interrupt":
            send({"id": rid, "result": {}})
            send({"method": "turn/completed",
                  "params": {"threadId": thread_id,
                             "turn": {"status": "interrupted"}}})
    return 0


if __name__ == "__main__":
    sys.exit(main())
