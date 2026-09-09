# 聊天附件使用每轮受信原件路径

Status: implemented

## 决策与理由

附件归属于 Workspace、Chat 和该 Chat 的 Agent。服务端保存原始字节与 SHA-256，浏览器仅提交 source_id 和摘要。Run 创建及重试均重新核对归属与原件内容，再把可信路径冻结为运行输入。既有 ModuleRunner 和 Runtime 继续执行；上传与交给 Agent 都不是已读取证据。

路径说明由 `runtime.EffectiveInstruction` 在选定本轮/历史/轮换输入后附加。用户原始 instruction 保留；每轮新增附件都能交给原生 resume 会话。文件发布采用受限根目录中的原子不可覆盖写入；并发重复上传通过逻辑 client key 返回同一 source。

## 放弃了什么

不建设固定 Office 摘要或专用模型调用链。用户已明确由 Agent 通过自己的工具读文件；工作台只承担原件保存、归属核验和读取事实的呈现。

不将路径仅写入 system prompt。部分 Runtime 只在首次创建会话时应用 persona，后续附件会因此无法交付。也不把路径拼进用户原文，避免增强上下文混入用户发言。

## 复活条件

若新增远程执行宿主，本地路径不可达时，需要在 Run 绑定的宿主资料访问能力中增加受控原件传输，再做相同的归属和摘要校验；不能把浏览器提交的任意路径当作授权。
