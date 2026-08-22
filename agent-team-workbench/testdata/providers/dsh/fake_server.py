#!/usr/bin/env python3
"""Fake DSH SDK JSON-RPC server：conformance / 适配器单测用录制回放桩。

协议面与 @deepseek-ai/dsh-sdk-jsonrpc-server 一致：
- initialize → serverInfo（wire-stable name）
- session/prompt → messageId 回执，随后推送 session.event / session.status 通知
- 支持 DSH_FAKE_FAIL=1 模拟 RPC 错误、DSH_FAKE_HANG=1 模拟挂起（取消测试）
"""
import json
import os
import sys
import time


def send(frame):
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def main():
    session_id = None
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except json.JSONDecodeError:
            continue
        method = req.get("method")
        req_id = req.get("id")

        if method == "initialize":
            if os.environ.get("DSH_FAKE_FAIL") == "init":
                send({"id": req_id, "error": {"code": -32000, "message": "fake init failure"}})
                continue
            send({"id": req_id, "result": {
                "serverInfo": {"name": "deepseek-harness-sdk-runtime", "version": "0.0.0-fake"}}})
        elif method == "session/prompt":
            params = req.get("params", {})
            session_id = params.get("sessionId")
            if os.environ.get("DSH_FAKE_HANG") == "1":
                # 只回执，不产生任何事件：用于进程级取消测试。
                send({"id": req_id, "result": {"messageId": "msg_fake_1"}})
                time.sleep(300)
                continue
            send({"id": req_id, "result": {"messageId": "msg_fake_1"}})
            send({"method": "session.status",
                  "params": {"sessionId": session_id, "status": "running"}})
            seq = 0
            for ev in [
                {"type": "turn/start", "turn": 1},
                {"type": "tool/call", "turn": 1, "step": 1, "callId": "c1",
                 "name": "bash", "arguments": "{\"cmd\":\"echo hi\"}"},
                {"type": "tool/result", "turn": 1, "step": 1,
                 "message": {"content": [{"type": "text", "text": "hi"}]}},
                {"type": "assistant/message", "turn": 1, "step": 1,
                 "message": {"content": [{"type": "text", "text": "fake dsh 完成"}]}},
                {"type": "turn/end", "turn": 1, "reason": "completed"},
            ]:
                seq += 1
                ev["seq"] = seq
                ev["time"] = int(time.time() * 1000)
                send({"method": "session.event",
                      "params": {"sessionId": session_id, "event": ev}})
            send({"method": "session.status",
                  "params": {"sessionId": session_id, "status": "idle"}})
        elif method == "shutdown":
            send({"id": req_id, "result": {}})
            return 0
    return 0


if __name__ == "__main__":
    sys.exit(main())
