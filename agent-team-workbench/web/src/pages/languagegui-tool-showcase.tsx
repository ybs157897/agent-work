import type { ChatMessage } from '../stores/chat.store';
import { ActivityGroup } from '../components/chat/tool-card';

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
    detail: 'export function ChatPage() {\n  const latestTimeline = useMemo(() => timeline, [timeline]);\n  return <TranscriptView segments={latestTimeline} />;\n}',
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
    detail: 'web/src/pages/chat.page.tsx:214: const latestTimeline = useMemo(() => timeline, [timeline]);\nweb/src/components/chat/transcript-view.tsx:35: export function TranscriptView({ segments, stoppedRuns }) {\nweb/src/stores/chat.store.ts:642: latestTimeline: TimelineEntry[]',
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
        defaultCollapsed={false}
        defaultSelectedKey="languagegui-tool-bash"
      />
    </section>
  );
}
