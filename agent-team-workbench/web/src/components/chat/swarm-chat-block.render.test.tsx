import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { CodexAgentCard, SwarmChatBlock, swarmMemberPreview, type SwarmProjection } from './swarm-chat-block';
import { SwarmMemberWorkspace, isSameSwarmMemberSelection, swarmAgentLabel } from './swarm-member-workspace';
import type { PresentedTranscriptSegment } from '../../utils/work-activity-timeline';

const projection: SwarmProjection = {
  id: 'swarm-1', runtime: 'kimi', title: '整理四个模块', total: 4, status: 'running', startedAt: '2026-08-29T00:00:00Z',
  members: [
    { id: 'a', index: 1, name: '勘探蜂', status: 'running', description: '扫描模块依赖', updatedAt: '2026-08-29T00:00:00Z' },
    { id: 'b', index: 2, name: '实现蜂', status: 'queued', description: '等待勘探蜂结果', reason: '依赖未齐', updatedAt: '2026-08-29T00:00:00Z' },
    { id: 'c', index: 3, name: '校验蜂', status: 'completed', description: '校验代码', summary: '已通过类型检查', updatedAt: '2026-08-29T00:00:00Z' },
    { id: 'd', index: 4, name: '记录蜂', status: 'failed', description: '记录结果', error: '无法读取变更清单', updatedAt: '2026-08-29T00:00:00Z' },
  ],
};

describe('SwarmChatBlock', () => {
  it('renders Codex child agent as a generic clickable card', () => {
    const html = renderToStaticMarkup(<CodexAgentCard agent={{ id: 'child-1', index: 1, runtime: 'codex', name: 'explore', description: '检查依赖', status: 'completed', summary: '依赖已确认', updatedAt: '2026-08-29T00:00:00Z' }} onSelect={() => undefined} />);
    expect(html).toContain('Codex 子 Agent explore');
    expect(html).toContain('已完成');
    expect(html).toContain('依赖已确认');
    expect(html).toContain('aria-label="Codex 子 Agent explore，已完成，依赖已确认"');
    expect(html).not.toContain('disabled=""');
    expect(html).not.toContain('Kimi 蜂群');
  });

  it('uses the shared side workspace for Codex thinking, tools and final output', () => {
    const segments: PresentedTranscriptSegment[] = [
      { kind: 'work-timeline', runId: 'run-codex', status: 'succeeded', createdAt: '2026-08-29T00:00:00Z', updatedAt: '2026-08-29T00:00:04Z', items: [
        { kind: 'thinking', msg: { key: 'thinking', runId: 'run-codex', kind: 'thinking', text: '先检查依赖', at: '2026-08-29T00:00:01Z' } },
        { kind: 'activity', runId: 'run-codex', items: [{ key: 'tool', runId: 'run-codex', kind: 'tool', text: '调用工具 Read', at: '2026-08-29T00:00:02Z', tool: 'Read', toolStatus: 'success', detail: 'package.json' }] },
      ] },
      { kind: 'assistant', msg: { key: 'final', runId: 'run-codex', kind: 'assistant', text: '依赖检查完成', at: '2026-08-29T00:00:04Z' } },
    ];
    const html = renderToStaticMarkup(<SwarmMemberWorkspace runId="run-codex" member={{ id: 'child-1', index: 1, runtime: 'codex', name: 'explore', description: '检查依赖', status: 'completed', updatedAt: '2026-08-29T00:00:04Z' }} segments={segments} onClose={() => undefined} />);
    expect(html).toContain('Codex 子 Agent 详情');
    expect(html).toContain('aria-label="你的消息"');
    expect(html).toContain('chat-user-card');
    expect(html).toContain('检查依赖');
    expect(html).not.toContain('Subagent ID');
    expect(html).not.toContain('父级 Run / Thread');
    expect(html).not.toContain('<dl');
    expect(html).toContain('1 段思考');
    expect(html).toContain('1 次工具');
    expect(html).toContain('依赖检查完成');
    expect(html).not.toContain('原始任务');
    expect(html).not.toContain('Agent 正文');
  });

  it('falls back to the same two-sided conversation without child transcript', () => {
    const html = renderToStaticMarkup(<SwarmMemberWorkspace runId="run-codex" member={{ id: 'child-1', index: 1, runtime: 'codex', name: 'explore', description: '检查依赖', status: 'failed', error: '子线程失败', updatedAt: '2026-08-29T00:00:04Z' }} onClose={() => undefined} />);
    expect(html).toContain('aria-label="你的消息"');
    expect(html).toContain('aria-label="探索 Agent 的消息"');
    expect(html).toContain('子线程失败');
    expect(html).not.toContain('生命周期摘要');
  });

  it('renders a concise member detail workspace without operational metadata', () => {
    expect(swarmAgentLabel('explore')).toBe('探索 Agent');
    const selected = { runId: 'run-1', swarmId: 'swarm-1', memberId: 'agent-1' };
    expect(isSameSwarmMemberSelection(selected, selected)).toBe(true);
    expect(isSameSwarmMemberSelection(selected, { ...selected, swarmId: 'swarm-2' })).toBe(false);
    const html = renderToStaticMarkup(<SwarmMemberWorkspace runId="run-1" member={{ ...projection.members[2]!, name: 'explore', summary: String.raw`答案是 \(x=4\)` }} onClose={() => undefined} />);
    expect(html).toContain('探索 Agent');
    expect(html).not.toContain('类型 explore');
    expect(html).not.toContain('run-1 / swarm-1');
    expect(html).toContain('class="katex"');
  });

  it('uses the shared transcript reader with the human child Agent identity', () => {
    const segments: PresentedTranscriptSegment[] = [{
      kind: 'assistant',
      msg: {
        key: 'child-final', runId: 'run-1', kind: 'assistant', text: '完整子 Agent 正文', at: '2026-08-29T00:00:01Z',
      },
    }];
    const html = renderToStaticMarkup(
      <SwarmMemberWorkspace
        runId="run-1"
        member={{ ...projection.members[2]!, name: 'explore' }}
        segments={segments}
        onClose={() => undefined}
      />,
    );
    expect(html).toContain('aria-label="你的消息"');
    expect(html).toContain('探索 Agent 的消息');
    expect(html).toContain('完整子 Agent 正文');
    expect(html).not.toContain('<h3');
  });
  it('turns Kimi Markdown and math into a bounded readable ticker', () => {
    const preview = swarmMemberPreview(String.raw`**题目1：** 解方程 \(2x + 5 = 13\) --- **最终答案：** \[\boxed{x = 4}\]`);
    expect(preview).toContain('题目1： 解方程 2x + 5 = 13 · 最终答案： x = 4');
    expect(preview).not.toMatch(/[\\*]/);
    expect(swarmMemberPreview('价格 $100')).toBe('价格 $100');
    expect(swarmMemberPreview('x'.repeat(120))).toHaveLength(96);
    expect(swarmMemberPreview('x'.repeat(120))).toMatch(/…$/);
  });

  it('renders the Kimi swarm header, accessible progress, two-column member grid and merge bar', () => {
    const html = renderToStaticMarkup(<SwarmChatBlock projection={projection} />);
    expect(html).toContain('Kimi 蜂群');
    expect(html).toContain('整理四个模块');
    expect(html).toContain('已完成');
    expect(html).toContain('role="progressbar"');
    expect(html).toContain('aria-valuenow="2"');
    expect(html).toContain('已有 1 个失败，仍等待 2 个结果');
    expect(html).toContain('已结束');
  });

  it('keeps all statuses textual and exposes detail controls with real facts only', () => {
    const html = renderToStaticMarkup(<SwarmChatBlock projection={projection} />);
    for (const label of ['排队中', '执行中', '已完成', '失败']) expect(html).toContain(label);
    expect(html).toContain('扫描模块依赖');
    expect(html).toContain('已通过类型检查');
    expect(html).toContain('>3</span>');
    expect(html).toContain('aria-pressed="false"');
    expect(html).toContain('disabled=""');
    expect(html).not.toContain('思考中');
    expect(html).not.toContain('工具调用');
  });

  it('marks the exact run, swarm and member selection without disabling production controls', () => {
    const html = renderToStaticMarkup(
      <SwarmChatBlock
        projection={projection}
        selectionPrefix="run-1"
        selectedMemberKey="run-1:swarm-1:c"
        onSelectMember={() => undefined}
      />,
    );
    expect(html).toContain('aria-pressed="true"');
    expect(html).toMatch(/_cellSelected_[^" ]+/);
    expect(html).not.toContain('disabled=""');
  });

  it('uses one atomic status region for the group', () => {
    const html = renderToStaticMarkup(<SwarmChatBlock projection={projection} />);
    expect(html.match(/role="status"/g)).toHaveLength(1);
    expect(html).toContain('aria-atomic="true"');
    expect(html).not.toMatch(/<div[^>]*role="status"[^>]*>.*<button/s);
  });

  it('does not call a completed parent swarm successful when a member failed', () => {
    const html = renderToStaticMarkup(<SwarmChatBlock projection={{ ...projection, status: 'completed' }} />);
    expect(html).toContain('已有 1 个失败，仍等待 2 个结果');
    expect(html).not.toContain('结果已齐套，可供巢头合流');
    expect(html).toMatch(/_mergeError_[^" ]+/);
    expect(html).not.toMatch(/_mergeComplete_[^" ]+/);
  });
});
