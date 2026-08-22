#!/usr/bin/env python3
"""Fake Claude Code CLI（stream-json 回放桩）。

模拟 print mode 输出：system.init → assistant → result。
CLAUDE_FAKE_FAIL=1 时输出 result subtype=error_during_execution。
"""
import json
import os
import sys


def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()


def main():
    send({"type": "system", "subtype": "init", "session_id": "sess_fake_1",
          "model": "fake-model", "cwd": "/tmp"})
    send({"type": "assistant", "message": {
        "role": "assistant",
        "content": [{"type": "text", "text": "fake claude 输出"}]}})
    if os.environ.get("CLAUDE_FAKE_FAIL") == "1":
        send({"type": "result", "subtype": "error_during_execution",
              "is_error": True, "result": "fake execution error"})
        return 1
    send({"type": "result", "subtype": "success", "is_error": False,
          "result": "done", "session_id": "sess_fake_1"})
    return 0


if __name__ == "__main__":
    sys.exit(main())
