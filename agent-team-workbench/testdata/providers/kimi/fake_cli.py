#!/usr/bin/env python3
"""Fake Kimi Code CLI（stream-json 回放桩）。

模拟 print mode：meta → assistant → result。
KIMI_FAKE_FAIL=1 时模拟真实 provider 错误：stderr 输出 error: 前缀行后流中断
（与真实 CLI 的 provider.auth_error 行为一致，验证 fail loud）。
"""
import json
import os
import sys


def send(o):
    sys.stdout.write(json.dumps(o) + "\n")
    sys.stdout.flush()


def main():
    send({"role": "meta", "type": "system.version", "version": "0.38.0"})
    if os.environ.get("KIMI_FAKE_FAIL") == "1":
        sys.stderr.write("error: failed to run prompt: provider.auth_error: 403 quota exceeded\n")
        sys.stderr.flush()
        return 0
    send({"role": "assistant", "text": "fake kimi 输出"})
    send({"role": "result", "type": "result", "text": "done", "is_error": False})
    return 0


if __name__ == "__main__":
    sys.exit(main())
