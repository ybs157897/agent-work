import type { ChatMessage } from '../stores/chat.store';
import { ActivityGroup } from '../components/chat/tool-card';
import { SwarmChatBlock, type SwarmProjection } from '../components/chat/swarm-chat-block';

/**
 * LanguageGUI 展台使用生产时间线消息，而不是另写一套 tool card。
 * 这些数据是明确标注的静态演示快照，不会触发任何真实工具执行。
 */
export const LANGUAGEGUI_TOOL_ITEMS: ChatMessage[] = [
  {
    key: 'languagegui-tool-bash',
    runId: 'languagegui-demo-run',
    kind: 'tool',
    text: '运行测试命令',
    argsSummary: 'go test ./internal/orchestrator/...',
    args: JSON.stringify({ command: 'go test ./internal/orchestrator/...' }),
    detail: 'ok   agent-work/internal/orchestrator  0.42s\nPASS',
    tool: 'bash',
    toolStatus: 'success',
    exitCode: 0,
    startedAt: '2026-08-27T08:00:00.000Z',
    completedAt: '2026-08-27T08:00:03.420Z',
    at: '2026-08-27T08:00:03.420Z',
  },
  {
    key: 'languagegui-tool-read',
    runId: 'languagegui-demo-run',
    kind: 'tool',
    text: '读取文件',
    argsSummary: 'web/src/pages/chat.page.tsx',
    args: JSON.stringify({ path: 'web/src/pages/chat.page.tsx' }),
    detail: 'export function ChatPage() {\n  const latestTimeline = useMemo(() => timeline, [timeline]);\n  return <AgentTranscriptReader segments={latestTimeline} />;\n}',
    tool: 'read',
    toolStatus: 'success',
    startedAt: '2026-08-27T08:00:04.000Z',
    completedAt: '2026-08-27T08:00:04.180Z',
    at: '2026-08-27T08:00:04.180Z',
  },
  {
    key: 'languagegui-tool-search',
    runId: 'languagegui-demo-run',
    kind: 'tool',
    text: '搜索相关实现',
    argsSummary: 'latestTimeline',
    args: JSON.stringify({ query: 'latestTimeline', path: 'web/src' }),
    detail: 'web/src/pages/chat.page.tsx:214: const latestTimeline = useMemo(() => timeline, [timeline]);\nweb/src/components/chat/transcript-view.tsx:35: export function AgentTranscriptReader({ segments }) {\nweb/src/stores/chat.store.ts:642: latestTimeline: TimelineEntry[]',
    tool: 'search',
    toolStatus: 'success',
    startedAt: '2026-08-27T08:00:05.000Z',
    completedAt: '2026-08-27T08:00:05.860Z',
    at: '2026-08-27T08:00:05.860Z',
  },
  {
    key: 'languagegui-tool-edit',
    runId: 'languagegui-demo-run',
    kind: 'tool',
    text: '应用修复',
    argsSummary: 'web/src/pages/chat.page.tsx',
    args: JSON.stringify({ path: 'web/src/pages/chat.page.tsx', old: '[timeline]', new: '[timeline]' }),
    detail: 'diff --git a/web/src/pages/chat.page.tsx b/web/src/pages/chat.page.tsx\n--- a/web/src/pages/chat.page.tsx\n+++ b/web/src/pages/chat.page.tsx\n@@ -211,7 +211,7 @@\n-  const latestTimeline = timeline.filter(Boolean);\n+  const latestTimeline = useMemo(() => timeline.filter(Boolean), [timeline]);',
    tool: 'edit',
    toolStatus: 'success',
    startedAt: '2026-08-27T08:00:06.000Z',
    completedAt: '2026-08-27T08:00:06.310Z',
    at: '2026-08-27T08:00:06.310Z',
  },
  {
    key: 'languagegui-tool-mcp',
    runId: 'languagegui-demo-run',
    kind: 'tool',
    text: '调用工具 github_review：',
    argsSummary: '读取当前 Pull Request 的评审状态',
    args: JSON.stringify({ repository: 'agent-work', pullRequest: 128 }),
    detail: '演示失败：连接到 GitHub MCP 服务超时（30s）',
    tool: 'github_mcp',
    toolStatus: 'failed',
    at: '2026-08-27T08:00:37.000Z',
  },
];

export const LANGUAGEGUI_SWARM_PROJECTION: SwarmProjection = {
  id: 'languagegui-demo-swarm',
  runtime: 'kimi',
  title: '架构评审 · 四路并行',
  total: 4,
  status: 'running',
  startedAt: '2026-08-29T08:00:00.000Z',
  members: [
    {
      id: 'demo-atlas', index: 1, name: 'Atlas', status: 'completed',
      description: '定位 application 与 adapters 的耦合点',
      summary: '已定位两个高风险依赖方向。', updatedAt: '2026-08-29T08:00:18.000Z',
    },
    {
      id: 'demo-forge', index: 2, name: 'Forge', status: 'running',
      description: '核对 ModuleRunner 终态与 resume 自愈',
      updatedAt: '2026-08-29T08:00:24.000Z',
    },
    {
      id: 'demo-pixel', index: 3, name: 'Pixel', status: 'waiting',
      description: '检查 Chat 投影和无障碍边界', reason: '等待运行时事件样本',
      updatedAt: '2026-08-29T08:00:25.000Z',
    },
    {
      id: 'demo-sentinel', index: 4, name: 'Sentinel', status: 'failed',
      description: '复核负向保证', error: '演示失败：证据尚未齐套',
      updatedAt: '2026-08-29T08:00:26.000Z',
    },
  ],
};

export function LanguageGuiToolShowcase() {
  return (
    <section className="lgui-tool-showcase" data-languagegui-tool-showcase>
      <div className="lgui-tool-showcase-head">
        <div>
          <span className="lgui-eyebrow">TOOL ACTIVITY · DEMO DATA</span>
          <h2>工具调用过程</h2>
        </div>
        <span className="lgui-tool-showcase-status">4 成功 · 1 失败</span>
      </div>
      <p className="lgui-tool-showcase-copy">复用生产 Chat 的 ActivityGroup。点击任意工具查看真实的终端、文件、搜索、Diff 或 MCP IN / OUT 详情。</p>
      <ActivityGroup
        items={LANGUAGEGUI_TOOL_ITEMS}
        defaultSelectedKey="languagegui-tool-bash"
      />
      <div className="mt-6" data-languagegui-swarm-showcase>
        <div className="lgui-tool-showcase-head">
          <div>
            <span className="lgui-eyebrow">KIMI AGENT SWARM · DEMO DATA</span>
            <h2>Kimi 蜂群正文</h2>
          </div>
          <span className="lgui-tool-showcase-status">生产组件</span>
        </div>
        <p className="lgui-tool-showcase-copy">仅显式 Kimi AgentSwarm 成员进入蜂巢；普通 Kimi/Codex 子 Agent 继续走单路 Agent Activity。</p>
        <SwarmChatBlock projection={LANGUAGEGUI_SWARM_PROJECTION} />
      </div>
    </section>
  );
}
