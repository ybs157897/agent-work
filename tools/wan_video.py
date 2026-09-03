#!/usr/bin/env python3
"""中转 API 的 wan 视频生成脚本（纯标准库，零依赖）。

给提示词，可选再给一张图（URL 或本地路径，本地自动转 base64），
调用中转 API 生成视频并下载到本地。

默认适配 tokenhub 类中转（如 https://tokenhub.../v1/video/generations）：
  提交 POST /v1/video/generations  ->  返回 task_id
  轮询 GET  /v1/video/generations/{task_id}  ->  data.data.output.video_url

配置（环境变量或命令行参数，参数优先）：
  WAN_API_BASE   中转地址，如 https://token.wasu.cn
  WAN_API_KEY    Key
  WAN_MODEL      模型名，默认 wan3.0-video

用法：
  # 文生视频
  python3 wan_video.py -p "一只猫在雨里跑，电影感"

  # 图生视频（图字段名若被中转忽略，会退化为文生视频）
  python3 wan_video.py -p "让图里的人转头微笑" --image ./a.png

  # 只看请求体不真发（核对契约用）
  python3 wan_video.py -p "test" --dry-run

契约对不上时脚本会打印原始返回，把那段贴回来即可适配。
"""
from __future__ import annotations

import argparse
import base64
import ipaddress
import json
import mimetypes
import os
import pathlib
import socket
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

DEFAULT_MODEL = "wan3.0-video"

TOKENHUB_SUBMIT_PATH = "/v1/video/generations"
TOKENHUB_TASK_PATH = "/v1/video/generations/{task_id}"

DASHSCOPE_SUBMIT_PATH = "/api/v1/services/aigc/video-generation/video-synthesis"
DASHSCOPE_TASK_PATH = "/api/v1/tasks/{task_id}"

POLL_INTERVAL = 8.0


def validate_url(url: str) -> str:
    """仅允许 https，且解析后的目标地址不得落在私网/环回/链路本地等网段。

    防 SSRF：中转地址与视频地址都不应指向内网服务或云元数据端点。
    """
    parts = urllib.parse.urlparse(url)
    if parts.scheme != "https":
        fail(f"仅允许 https URL：{url}")
    host = parts.hostname
    if not host:
        fail(f"URL 缺少主机名：{url}")
    try:
        infos = socket.getaddrinfo(host, parts.port or 443, type=socket.SOCK_STREAM)
    except socket.gaierror:
        fail(f"无法解析主机：{host}")
    for info in infos:
        ip = ipaddress.ip_address(info[4][0])
        if (ip.is_private or ip.is_loopback or ip.is_link_local
                or ip.is_reserved or ip.is_multicast or ip.is_unspecified):
            fail(f"目标地址落在禁用的网段：{host} -> {ip}")
    return url


class _ValidatingRedirectHandler(urllib.request.HTTPRedirectHandler):
    """重定向目标同样过 URL 校验，防止经 302 跳到内网。"""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        validate_url(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


_OPENER = urllib.request.build_opener(_ValidatingRedirectHandler())


def safe_output_path(out: str) -> str:
    """输出文件限制在当前工作目录内：拒绝绝对路径与 .. 跳出。"""
    if os.path.isabs(out):
        fail(f"输出路径必须是相对路径：{out}")
    norm = os.path.normpath(out)
    if norm == ".." or norm.startswith(".." + os.sep) or os.path.isabs(norm):
        fail(f"输出路径不允许跳出当前目录：{out}")
    return norm


def http_json(method: str, url: str, key: str, body: dict | None = None,
              extra_headers: dict | None = None) -> tuple[int, object]:
    url = validate_url(url)
    headers = {"Authorization": f"Bearer {key}"}
    if extra_headers:
        headers.update(extra_headers)
    data = None
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with _OPENER.open(req, timeout=120) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as e:
        raw = e.read()
        status = e.code
    try:
        return status, json.loads(raw.decode("utf-8"))
    except Exception:
        return status, raw


def image_value(image: str) -> str:
    """URL 原样透传；本地文件转 base64 data URI（部分中转支持）。"""
    if image.startswith(("http://", "https://")):
        return image
    mime = mimetypes.guess_type(image)[0] or "image/png"
    with open(image, "rb") as f:
        b64 = base64.b64encode(f.read()).decode("ascii")
    return f"data:{mime};base64,{b64}"


def fail(msg: str, resp: object = None) -> None:
    print(f"[错误] {msg}", file=sys.stderr)
    if resp is not None:
        text = resp if isinstance(resp, (str, bytes)) else json.dumps(resp, ensure_ascii=False)
        if isinstance(text, bytes):
            text = text.decode("utf-8", "replace")
        print(text[:4000], file=sys.stderr)
    sys.exit(1)


def download(url: str, out: str) -> None:
    url = validate_url(url)
    target = pathlib.Path(safe_output_path(out))
    req = urllib.request.Request(url, headers={"User-Agent": "wan-video-script/1.0"})
    with _OPENER.open(req, timeout=300) as resp, target.open("wb") as f:
        while True:
            chunk = resp.read(1 << 16)
            if not chunk:
                break
            f.write(chunk)


# --- tokenhub（默认） -------------------------------------------------------
def submit_tokenhub(args, base: str, key: str) -> str:
    body: dict = {"model": args.model, "prompt": args.prompt}
    if args.image:
        body["image_url"] = image_value(args.image)
    if args.dry_run:
        print(json.dumps({"url": base + TOKENHUB_SUBMIT_PATH, "body": body},
                         ensure_ascii=False, indent=2))
        sys.exit(0)
    status, resp = http_json("POST", base + TOKENHUB_SUBMIT_PATH, key, body)
    if status >= 300 or not isinstance(resp, dict):
        fail(f"提交失败 HTTP {status}", resp)
    task_id = resp.get("task_id") or resp.get("id")
    if not task_id:
        fail("提交成功但没拿到 task_id，原始返回：", resp)
    return str(task_id)


def _tokenhub_output(resp: object) -> dict:
    if not isinstance(resp, dict):
        return {}
    return ((resp.get("data") or {}).get("data") or {}).get("output") or {}


def poll_tokenhub(base: str, key: str, task_id: str, timeout: int) -> str:
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, resp = http_json("GET", base + TOKENHUB_TASK_PATH.format(task_id=task_id), key)
        out = _tokenhub_output(resp)
        st = str(out.get("task_status") or "").upper()
        print(f"  任务状态: {st or 'UNKNOWN'}", flush=True)
        if st in ("SUCCEEDED", "SUCCESS"):
            url = out.get("video_url") or extract_video_url_generic(out)
            if url:
                return str(url)
            fail("任务成功但没找到视频地址，原始返回：", resp)
        if st in ("FAILED", "CANCELED", "CANCELLED", "UNKNOWN"):
            fail("任务失败，原始返回：", resp)
        time.sleep(POLL_INTERVAL)
    fail(f"轮询超时（{timeout}s），task_id={task_id}，可凭 id 去中转控制台找产物")


# --- dashscope（备选） ------------------------------------------------------
def extract_video_url_generic(out: dict) -> str | None:
    for key in ("video_url", "url"):
        if out.get(key):
            return str(out[key])
    results = out.get("results") or out.get("videos") or []
    if isinstance(results, list) and results and isinstance(results[0], dict):
        for key in ("url", "video_url"):
            if results[0].get(key):
                return str(results[0][key])
    return None


def submit_dashscope(args, base: str, key: str) -> str:
    body: dict = {"model": args.model, "input": {"prompt": args.prompt},
                  "parameters": {"size": args.size, "duration": args.duration}}
    if args.image:
        body["input"]["img_url"] = image_value(args.image)
    if args.dry_run:
        print(json.dumps({"url": base + DASHSCOPE_SUBMIT_PATH, "body": body},
                         ensure_ascii=False, indent=2))
        sys.exit(0)
    status, resp = http_json("POST", base + DASHSCOPE_SUBMIT_PATH, key, body,
                             extra_headers={"X-DashScope-Async": "enable"})
    if status >= 300 or not isinstance(resp, dict):
        fail(f"提交失败 HTTP {status}", resp)
    task_id = ((resp.get("output") or {}).get("task_id")) or resp.get("id")
    if not task_id:
        fail("提交成功但没拿到 task_id，原始返回：", resp)
    return str(task_id)


def poll_dashscope(base: str, key: str, task_id: str, timeout: int) -> str:
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, resp = http_json("GET", base + DASHSCOPE_TASK_PATH.format(task_id=task_id), key)
        out = (resp.get("output") or {}) if isinstance(resp, dict) else {}
        st = str(out.get("task_status") or "").upper()
        print(f"  任务状态: {st or 'UNKNOWN'}", flush=True)
        if st in ("SUCCEEDED", "SUCCESS"):
            url = extract_video_url_generic(out)
            if url:
                return url
            fail("任务成功但没找到视频地址，原始返回：", resp)
        if st in ("FAILED", "CANCELED", "CANCELLED", "UNKNOWN"):
            fail("任务失败，原始返回：", resp)
        time.sleep(POLL_INTERVAL)
    fail(f"轮询超时（{timeout}s），task_id={task_id}")


def main() -> None:
    p = argparse.ArgumentParser(description="中转 API wan 视频生成（提示词 + 可选图片）")
    p.add_argument("-p", "--prompt", required=True, help="视频提示词")
    p.add_argument("--image", help="参考图：URL 或本地路径（图生视频）")
    p.add_argument("--base", default=os.environ.get("WAN_API_BASE", ""),
                   help="中转地址（或环境变量 WAN_API_BASE）")
    p.add_argument("--key", default=os.environ.get("WAN_API_KEY", ""),
                   help="Key（或环境变量 WAN_API_KEY）")
    p.add_argument("--model", default=os.environ.get("WAN_MODEL", DEFAULT_MODEL),
                   help=f"模型名，默认 {DEFAULT_MODEL}")
    p.add_argument("--size", default="1280*720", help="尺寸（dashscope 协议用）")
    p.add_argument("--duration", type=int, default=5, help="时长秒（dashscope 协议用）")
    p.add_argument("--protocol", choices=["tokenhub", "dashscope"], default="tokenhub",
                   help="中转契约：tokenhub（默认）或 dashscope")
    p.add_argument("--timeout", type=int, default=900, help="轮询超时秒数，默认 900")
    p.add_argument("-o", "--output", help="输出文件，默认 wan_<时间戳>.mp4")
    p.add_argument("--dry-run", action="store_true", help="只打印请求体，不真发")
    args = p.parse_args()

    if not args.dry_run and (not args.base or not args.key):
        fail("缺少中转地址或 Key：用 --base/--key 或环境变量 WAN_API_BASE/WAN_API_KEY")
    base = args.base.rstrip("/")

    print(f"提交任务：model={args.model} protocol={args.protocol} "
          f"image={'有' if args.image else '无'}")
    if args.protocol == "tokenhub":
        task_id = submit_tokenhub(args, base, args.key)
        video_url = poll_tokenhub(base, args.key, task_id, args.timeout)
    else:
        task_id = submit_dashscope(args, base, args.key)
        video_url = poll_dashscope(base, args.key, task_id, args.timeout)

    out = args.output or time.strftime("wan_%Y%m%d_%H%M%S.mp4")
    print(f"  task_id={task_id}\n下载视频 -> {out}")
    download(video_url, out)
    print(f"完成：{out}")


if __name__ == "__main__":
    main()
