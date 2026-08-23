# kimiapp adapter：网关型接 kap-server，不接 ACP/print-CLI

Status: implemented

## 决策与理由

Kimi Code 的服务端能力（`kimi web`，kap-server：REST /api/v1 + 单 WS /api/v1/ws mux）以网关形态接入（`internal/runtime/adapters/kimiapp/`，AdapterID `kimi-appserver`），镜像 dsh 的 boujoy 形态：supervisor 拉起/守护进程、独立 KIMI_CODE_HOME、读 `server.token` 鉴权、崩溃退避重启。resume 依赖服务端懒恢复（旧 session id 直接 prompt 即磁盘重建），探测失败报 session_unknown 走自愈（遵守 [resume-never-silent-degrade](2026-08-23-resume-never-silent-degrade.md)）。

依据：kap-server 的 resume/steering/审批/turn 级取消四项能力完整，比 print-mode CLI（kimi adapter）高出一档；WS 事件流带 seq/epoch 游标可断线续传；与 dsh 共用网关运维心智。

## 放弃了什么

- **ACP stdio（acp-server）**：Paperclip 偏好 ACP，但 kimi 的 ACP host 面向编辑器场景（fs/terminal 桥接），事件粒度与审批模型不如 kap-server 完整，且每 run 一进程的生命周期与「会话连续性交给 harness 磁盘」的目标相悖。
- **扩展现有 kimi print-CLI adapter**：print 模式结构性无 steering/审批/精确取消，保留为无 `kimi web` 环境下的降级路径而非主路径。
- **WS abort 帧取消**：schema 有定义但服务端 switch 未分发（wsConnectionV1），取消只能走 REST `:abort`。

## 妥协与复活条件

- `system_prompt` 只声明 CapAdapterTranslated：服务端 `applySessionAgentConfig` 当前忽略该字段（前向透传，服务端支持后自动生效）。若 kap-server 落地 system_prompt → 升级 supported，返工点 `kimiapp.go` Manifest。
- WS 重连收到 `resync_required` 时未做二次同步（durable 事件可能有缺口）。若出现「重连后丢审批/终态」类报告 → 返工点 `wire.go` 重订逻辑，按游标回退 REST `/messages` 补齐。
- 受管模式显式指定端口被占时 kap-server 自动 +1 换口而 supervisor 轮询错端口——生产配置保持 `Port=0` 动态口；若必须定端口 → 返工点 `supervisor.go` waitReady 改解析进程实际监听口。
