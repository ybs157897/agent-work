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

describe('ActivityGroup · ordered compact tool log', () => {
  it('展示组级状态、纵向工具行，并把选中详情紧跟在对应行后', () => {
    const html = renderToStaticMarkup(
      <ActivityGroup items={items} defaultCollapsed={false} defaultSelectedKey="bash" />,
    );

    expect(html).toContain('data-state="error"');
    expect(html).toContain('工具调用');
    expect(html).toContain('role="heading" aria-level="3"');
    expect(html).toContain('1 完成 · 1 失败');
    expect(html).toContain('执行失败');
    expect(html).toContain('role="list" aria-label="工具调用"');
    expect(html).toContain('Action 1');
    expect(html).toContain('Action 2');
    expect(html).toContain('已完成');
    expect(html).toContain('失败');
    expect(html).toContain('aria-expanded="true"');
    expect(html).toContain('aria-controls=');
    expect(html).not.toContain('aria-pressed=');
    expect(html).toContain('pnpm test');
    expect(html).toContain('57 files passed');
    expect(html).toContain('role="region" aria-label="Bash 工具详情"');
    expect(html.indexOf('role="region" aria-label="Bash 工具详情"')).toBeLessThan(html.indexOf('Action 2'));
  });

  it('历史大组可默认折叠但仍保留数量与终态，空组给出明确空态', () => {
    const collapsed = renderToStaticMarkup(
      <ActivityGroup items={items} defaultCollapsed />,
    );
    expect(collapsed).toContain('aria-label="展开工具调用：共 2 次；Bash × 1 · Tool call × 1；1 完成 · 1 失败"');
    expect(collapsed).toContain('aria-expanded="false"');
    expect(collapsed).not.toContain('role="list" aria-label="工具调用"');
    // 折叠态 body 未挂载，aria-controls 不得悬空指向不存在的元素。
    expect(collapsed).not.toContain('aria-controls');

    const expanded = renderToStaticMarkup(<ActivityGroup items={items} defaultCollapsed={false} />);
    expect(expanded).toContain('aria-controls');

    const defaultState = renderToStaticMarkup(<ActivityGroup items={items} />);
    expect(defaultState).toContain('2 次调用');
    expect(defaultState).toContain('Bash × 1 · Tool call × 1');
    expect(defaultState).toContain('aria-expanded="false"');
    expect(defaultState).toContain('aria-label="展开工具调用：共 2 次；Bash × 1 · Tool call × 1；1 完成 · 1 失败"');
    expect(defaultState).not.toContain('role="list" aria-label="工具调用"');

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
      <ActivityGroup items={[waiting]} defaultCollapsed={false} defaultSelectedKey="read-waiting" />,
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
      <ActivityGroup items={[edit]} defaultCollapsed={false} defaultSelectedKey="edit-diff" suppressDiff />,
    );
    expect(suppressedHtml).toContain('变更内容已汇总到本轮 Diff');
    expect(suppressedHtml).not.toContain('class="chat-diff"');
  });

  it('timeline 折叠行展示最新工具的人类摘要，并保留组级状态', () => {
    const html = renderToStaticMarkup(<ActivityGroup items={items} variant="timeline" />);
    expect(html).toContain('data-variant="timeline"');
    expect(html).toContain('sr-only');
    expect(html).not.toContain('>工具执行<');
    expect(html).toContain('Tool failed');
    expect(html).toContain('1 完成 · 1 失败');
    expect(html).toContain('title="Tool failed"');
    expect(html).toContain('aria-expanded="false"');
  });

  it('started 事件到达时即显示 running 工具，后续 started 可增量加入同一组', () => {
    const running: ChatMessage = {
      key: 'started-1', runId: 'run-3', kind: 'tool', text: '调用工具 read',
      tool: 'read', toolStatus: 'running', argsSummary: 'src/App.tsx', at: '2026-08-27T08:00:00.000Z',
    };
    const laterRunning: ChatMessage = { ...running, key: 'started-2', tool: 'bash', argsSummary: 'pnpm test' };
    const html = renderToStaticMarkup(<ActivityGroup items={[running, laterRunning]} variant="timeline" />);
    expect(html).toContain('2 次调用');
    expect(html).toContain('2 进行中');
    expect(html).toContain('正在执行');
    expect(html).not.toContain('role="list" aria-label="工具调用"');
  });

  it('Write 行把 diff 统计渲染为灰色摘要与彩色增删数字', () => {
    const write: ChatMessage = {
      key: 'write-diff', runId: 'run-3', kind: 'tool', tool: 'Write', toolStatus: 'success', at: '',
      text: '调用工具 Write：knowledge/prd/evolution-roadmap-next-phase.md',
      detail: 'diff --git a/a.md b/a.md\n--- a/a.md\n+++ b/a.md\n@@ -1 +1,2 @@\n-old\n+new\n+line',
    };
    const html = renderToStaticMarkup(<ActivityGroup items={[write]} defaultCollapsed={false} />);
    expect(html).toContain('1 个文件已更改');
    expect(html).toMatch(/changeAddition[^\"]*\"> \+2<\/span>/);
    expect(html).toMatch(/changeDeletion[^\"]*\"> −1<\/span>/);
  });

  it('Write bytes 回退展示文件与大小，不生成虚假的增删行数', () => {
    const write: ChatMessage = {
      key: 'write-bytes', runId: 'run-3', kind: 'tool', tool: 'Write', toolStatus: 'success', at: '',
      text: '调用工具 Write：knowledge/prd/evolution-roadmap-next-phase.md',
      detail: 'Wrote 24832 bytes to knowledge/prd/evolution-roadmap-next-phase.md',
    };
    const html = renderToStaticMarkup(<ActivityGroup items={[write]} defaultCollapsed={false} />);
    expect(html).toContain('1 个文件已更改');
    expect(html).toContain('24.8 KB');
    expect(html).not.toContain('changeAddition');
    expect(html).not.toContain('changeDeletion');

    const collapsed = renderToStaticMarkup(<ActivityGroup items={[write]} variant="timeline" />);
    expect(collapsed).toContain('1 个文件已更改');
    expect(collapsed).toContain('24.8 KB');
    expect(collapsed).not.toContain('Updated Writing');
  });
});
