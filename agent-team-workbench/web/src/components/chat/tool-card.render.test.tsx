import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../../stores/chat.store';
import { ActivityGroup } from './tool-card';

const items: ChatMessage[] = [
  {
    key: 'bash',
    runId: 'run-1',
    kind: 'tool',
    text: '调用工具 bash：pnpm test',
    argsSummary: 'pnpm test',
    detail: '57 files passed',
    tool: 'bash',
    toolStatus: 'success',
    exitCode: 0,
    startedAt: '2026-08-27T08:00:00.000Z',
    completedAt: '2026-08-27T08:00:01.250Z',
    at: '2026-08-27T08:00:01.250Z',
  },
  {
    key: 'mcp',
    runId: 'run-1',
    kind: 'tool',
    text: '调用工具 github_mcp：读取评审',
    argsSummary: '读取评审',
    args: '{"pullRequest":128}',
    detail: '连接超时',
    tool: 'github_mcp',
    toolStatus: 'failed',
    at: '2026-08-27T08:00:31.250Z',
  },
];

describe('ActivityGroup · LanguageGUI render', () => {
  it('展示组级状态、Action 卡、可见状态文字和选中工具详情', () => {
    const html = renderToStaticMarkup(
      <ActivityGroup items={items} defaultSelectedKey="bash" />,
    );

    expect(html).toContain('data-state="error"');
    expect(html).toContain('工具执行');
    expect(html).toContain('1 完成 · 1 失败');
    expect(html).toContain('执行失败');
    expect(html).toContain('role="list" aria-label="工具调用"');
    expect(html).toContain('Action 1');
    expect(html).toContain('Action 2');
    expect(html).toContain('已完成');
    expect(html).toContain('失败');
    expect(html).toContain('aria-expanded="true"');
    expect(html).toContain('pnpm test');
    expect(html).toContain('57 files passed');
    expect(html).toContain('role="region" aria-label="Bash 工具详情"');
  });

  it('历史大组可默认折叠但仍保留数量与终态，空组给出明确空态', () => {
    const collapsed = renderToStaticMarkup(
      <ActivityGroup items={items} defaultCollapsed />,
    );
    expect(collapsed).toContain('aria-label="展开 2 个工具调用"');
    expect(collapsed).toContain('aria-expanded="false"');
    expect(collapsed).not.toContain('role="list" aria-label="工具调用"');

    const empty = renderToStaticMarkup(<ActivityGroup items={[]} />);
    expect(empty).toContain('等待调用');
    expect(empty).toContain('当前回合没有工具调用');
    expect(empty).not.toContain('aria-label="收起工具调用"');
  });

  it('运行中无输出与已汇总 Diff 都给出真实空态，不产生空白详情区', () => {
    const waiting: ChatMessage = {
      key: 'read-waiting',
      runId: 'run-2',
      kind: 'tool',
      text: '调用工具 read：src/App.tsx',
      argsSummary: 'src/App.tsx',
      args: '{"path":"src/App.tsx"}',
      tool: 'read',
      toolStatus: 'running',
      at: '2026-08-27T08:00:00.000Z',
    };
    const waitingHtml = renderToStaticMarkup(
      <ActivityGroup items={[waiting]} defaultSelectedKey="read-waiting" />,
    );
    expect(waitingHtml).toContain('等待工具输出…');

    const edit: ChatMessage = {
      key: 'edit-diff',
      runId: 'run-2',
      kind: 'tool',
      text: '调用工具 edit：src/App.tsx',
      argsSummary: 'src/App.tsx',
      tool: 'edit',
      toolStatus: 'success',
      detail: 'diff --git a/src/App.tsx b/src/App.tsx\n--- a/src/App.tsx\n+++ b/src/App.tsx\n@@ -1 +1 @@\n-old\n+new',
      at: '2026-08-27T08:00:01.000Z',
    };
    const suppressedHtml = renderToStaticMarkup(
      <ActivityGroup items={[edit]} defaultSelectedKey="edit-diff" suppressDiff />,
    );
    expect(suppressedHtml).toContain('变更内容已汇总到本轮 Diff');
    expect(suppressedHtml).not.toContain('class="chat-diff"');
  });
});
