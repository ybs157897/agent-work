# 对话页体验精修：回合头/气泡表面/空态脚手架/composer 停止入坞

Status: implemented

输入：知乎开放平台 CLI 站内调研（AG-UI 活动面板分离、Chat UI≠Agent UI、
生成式 UI 83% 偏好实验）+ 本地浏览器实测诊断（transcript 文档化、全白无
层次、空态冷、composer 素）。方案见
`agent-team-workbench-docs/frontend-design-md-redesign.md` §6。

## 决策与理由

1. **回合头常显而非悬停显时间**：多智能体场景下"这轮是谁说的"是身份
   问题不是装饰，常显头像+名字+时间；悬停操作条（MessageActions）保留
   复制/分叉，去掉时钟避免双份。
2. **用户气泡是品牌浅底的唯一新消费点**：`brand-primary/7%` 底 + 20% 边，
   DESIGN.md frontmatter 已写明这是用户侧专属用法，防止品牌浅底被滥用。
3. **停止按钮从页头入坞 composer**：对齐 ChatGPT 心智（控制权在输入处），
   页头只留状态文字，避免双停止按钮。
4. **空会话脚手架按角色出建议 chips**：pm/architect/developer/ui/reviewer
   各两条，未知角色回落通用；点击填入输入框并聚焦——首条消息是冷启动
   最大摩擦点。
5. **顺手修真 bug**：conversationLabel 的「待审批」分支原在 ACTIVE 判定后
   不可达（waiting_approval ∈ ACTIVE），等待审批的会话一直显示「思考中…」；
   状态点同规则（waiting_approval 先判，警示黄盖过执行脉冲）。防回归断言
   钉在 `chat-session-visuals.test.ts`。

## 放弃了什么

- **assistant-ui 迁移**：病根是呈现层未穿设计系统，不是原语缺失；协议层
  （SSE 游标/回合槽位/审批粒度）是资产，重适配无收益。分支/附件等能力
  需求出现时再评估。
- **生成式 UI（A2UI）**：LATER。等审批/表单类交互密度上升后再评估。
- **活动/消息分离重构**：ActivityGroup 已有终态自动收拢，本刀不动。
- **chat transcript 渲染区规格**：仍归 codex 逆向文档管，本刀只加回合头
  与气泡表面，未动 markdown 槽位。

## 防回归断言

- `chat-session-visuals.test.ts`：状态点九态映射 + conversationLabel
  waiting_approval→「待审批」次序断言。

## 验证

pnpm tsc --noEmit / 319 Vitest 全绿 / lint 0 errors；浏览器实测截图确认
回合头、气泡表面、chips、状态点、composer 停止位全部生效（vite HMR，
会话「帮我review一下当前项目代码」）。
