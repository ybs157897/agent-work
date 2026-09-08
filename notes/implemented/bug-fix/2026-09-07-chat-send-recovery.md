# 对话发送失败保留原文与重试入口

Status: implemented

## 决策与理由

现存 Nova 对话“当前知识库有什么东西”显示 0 轮待开始。前端 `doSend` 先清空草稿，`send`/`drainQueue` 在 createRun 前移除待发内容，失败只弹短暂 toast，用户既看不到常驻失败原因，也无法恢复原文。对话请求尚未形成 Run 的失败应在输入区显示，保留原文和幂等键，允许处理配置后继续发送。

使用现有待发送队列承担重试，不创建虚构 Run 或 assistant 错误消息。旧请求在工作区/智能体/会话切换后不得把失败和队列写入新的会话；失败不触发自动重试。

## 放弃了什么

不把模型或运行时错误当作成功消息；不依赖会自动消失的通知承担恢复；不重新生成幂等键导致请求结果不确定时重复执行。队列仍保持现有内存生命周期，持久化队列不属于本次修改。

## 当前环境证据与连接恢复

原预览 `53225` 的 `knowledge-smoke` mount 广告 generation 为 `3495bd...7e3bc`，工作区 location 仍为 `9b4884...407e7`（2026-09-06 创建的 version 1）。目录搬迁改变了本机注册表指纹，但没有通过正式 PATCH 重新绑定，CreateRun 报 `workspace_mount_generation_changed`。设置页补充连接状态读取、探测和对当前已广告且 repository identity 相同的 mount 显式重新连接；绝不跳过 generation 校验或替换成任意磁盘路径。
