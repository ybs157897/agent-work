# cmdc-bridge — Command Code → Anthropic Messages 协议桥

把 `cmdc`（Command Code CLI）的私有推理端点翻译成标准 **Anthropic Messages API**，
让你手上已有的 cmdc key 能喂给任何说 Anthropic 协议的客户端（ZCode / Claude Code /
opencode / Cline …），模型固定用 `deepseek/deepseek-v4-flash`。

## 启动

```bash
# 直接跑（key 自动读 ~/.commandcode/auth.json）
PORT=8799 node tools/cmdc-bridge.mjs

# 或显式指定 key / 上游 / 模型
PORT=8799 CMDC_KEY=user_xxx CMDC_MODEL=deepseek/deepseek-v4-flash node tools/cmdc-bridge.mjs
```

健康检查：`curl http://127.0.0.1:8799/health`

## 端点

| 路由 | 说明 |
|---|---|
| `POST /v1/messages` | Anthropic Messages，支持 `stream: true/false` |
| `GET /v1/models` | 固定返回 deepseek-v4-flash（照顾做模型探测的客户端） |
| `GET /health` | 存活检查 |

## 客户端接入

**Claude Code CLI（环境变量，最省事）**：

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:8799 \
ANTHROPIC_AUTH_TOKEN=任意非空值 \
claude --model deepseek/deepseek-v4-flash
```

key 可以不传给客户端——桥默认从 `~/.commandcode/auth.json` 取；客户端传了
`x-api-key` / `Authorization` 时以客户端为准。

**ZCode / 其他 GUI 客户端**：在模型供应商设置里选 Anthropic 兼容自定义端点，
填 baseURL `http://127.0.0.1:8799` + 任意 API key + 模型
`deepseek/deepseek-v4-flash`。若客户端不支持自定义 provider，就走上面的 env 通道。

## 已逆向的 wire 协议（2026-09-02，CLI v1.40.1）

- 上游：`POST https://api.commandcode.ai/alpha/generate`（Cloudflare 后面）
- 必需头：`Authorization: Bearer <key>`、`x-command-code-version: 1.40.1`
  （缺失 → 403 upgrade_required）、`x-session-id`
- 信封：`{ config{…, structure:[] 必需}, memory, taste, skills,
  permissionMode("default"|"standard"|"auto-accept"|"plan"|"bypass"), threadId,
  params }`；`params` 是 Anthropic 形状（model/messages/tools/system/max_tokens/stream），
  但内容块为 AI SDK 风格（`tool-call`/`reasoning`，工具结果用独立 `role:"tool"`
  消息 + `output:{type:"text",value}`）
- 响应：NDJSON SSE，事件族
  `start / start-step / reasoning-{start,delta,end} / text-{start,delta,end} /
  tool-input-{start,delta,end} / tool-call / provider-metadata / finish-step / finish`

## 风险与维护

- `/alpha/*` 是未公开的 alpha 面，随时可能改版。桥对未知事件宽容、对校验错误
  fail-fast：看到上游 4xx 把 `message` 字段里的 zod 提示对着改信封字段即可。
- 协议逆向仅用于让**你自己的账号 key**在你自己的机器上互操作，上游可观测
  （provider-metadata 里带 cost/审计明细）。别拿去做共享池/转售。
- 上游会把请求转发给 DeepSeek 官方（缓存命中/计费明细在 `provider-metadata` 可见）。

## 守护进程（launchd，已部署）

已装 LaunchAgent `com.yin.cmdc-bridge`：登录自启、崩溃 5 秒内自动拉起。
**运行副本在 `~/Library/cmdc-bridge/cmdc-bridge.mjs`**（`~/Documents` 受 TCC 保护，
launchd 起的 node 无授权会卡死在脚本读取上，故部署副本必须放 TCC 盲区外）。

```bash
# 改了 tools/cmdc-bridge.mjs 后同步部署 + 重启
cp tools/cmdc-bridge.mjs ~/Library/cmdc-bridge/cmdc-bridge.mjs && \
  launchctl kickstart -k gui/$(id -u)/com.yin.cmdc-bridge

launchctl kickstart -k gui/$(id -u)/com.yin.cmdc-bridge   # 重启
launchctl bootout gui/$(id -u)/com.yin.cmdc-bridge        # 停止（开机不再自启）
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.yin.cmdc-bridge.plist  # 恢复
tail -f ~/Library/Logs/cmdc-bridge.log                    # 日志
```

## 已验证

非流式文本、流式文本、工具调用（`input_json_delta` 流式组装 + `stop_reason=tool_use`）、
多轮 `tool_result` 回传，四条链路全绿（2026-09-02）。
