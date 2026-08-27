# LanguageGUI ContentBlock 正文协议

Status: implemented

## 决策与理由

结构化正文使用版本化、白名单式的 `languagegui/v1` ContentBlock 文档。生产入口有两条：
canonical `message.completed.data.content_blocks` 可直接携带文档；尚未支持结构化事件的
Runtime 可在 Markdown 中输出 `languagegui` fenced JSON。两条入口共用同一个防御式解析器
和渲染器；通用块包括 metric、table、chart、file、event、image、audio、map、search。

解析器限制块数、行列数、序列数、数据点数和字符串长度；未知类型或无效字段不进入
React 树。Markdown fence 解析失败时必须回落为普通代码块，不能吞掉模型原始输出。
结构化块只使用已知字段，不开放 HTML、任意 className、任意组件名或脚本。

## 放弃了什么

不为汇率、天气、股票等每个演示领域定义一个独立 wire schema；`content-block-templates.ts`
把它们组合为 metric、chart、table、event、media 等通用原语。不把整条助手消息强制改成 JSON，因为普通 Markdown 仍是
最可靠的文本载体，也不要求所有 Runtime 同时升级协议。

不在前端按自然语言猜测卡片类型。猜测会让相同回答在回放时产生不同 UI，也无法为错误
结果提供可审计的原始载荷。

## 复活条件

【canonical 事件在所有 Runtime 中稳定携带 typed content parts，且历史数据已迁移】
→ 移除 fenced JSON 入口 → 保留同一 `languagegui/v1` 解析器作为事件边界校验器。

【出现无法由五类通用原语组合、并且至少有两个真实消费场景的领域组件】
→ 扩展版本化 union → 先补 schema、上限与 fallback 测试，再新增渲染器。
