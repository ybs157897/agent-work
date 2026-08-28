import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ChatBottomDock } from './chat-bottom-dock';

describe('ChatBottomDock LanguageGUI workflow', () => {
  it('renders one semantic workflow with goal progress and ordered Action cards', () => {
    const html = renderToStaticMarkup(
      <ChatBottomDock
        workflow={{
          goal: { objective: '交付完整的正文展示', status: 'active' },
          steps: [
            { step: '核对设计原语', status: 'completed' },
            { step: '实现工作流卡片', status: 'in_progress' },
            { step: '完成视觉验收', status: 'pending' },
          ],
          proposedPlan: '# 方案\n\n按阶段完成并验证。',
        }}
      />,
    );

    expect(html).toContain('aria-label="任务工作流"');
    expect(html).toContain('当前目标');
    expect(html).toContain('交付完整的正文展示');
    expect(html).toContain('aria-label="目标执行进度"');
    expect(html).toContain('aria-valuenow="1"');
    expect(html).toContain('本轮执行计划');
    expect(html).toContain('Action 1');
    expect(html).toContain('Action 2');
    expect(html).toContain('Action 3');
    expect(html).toContain('aria-current="step"');
    expect(html).toContain('已完成');
    expect(html).toContain('进行中');
    expect(html).toContain('待处理');
    expect(html).toContain('方案草稿');
    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain('配置 Action');
    expect(html).not.toContain('删除 Action');
    expect(html).not.toContain('添加执行步骤');
  });

  it('keeps a standalone Plan-mode proposal expanded and maps limited goal states', () => {
    const proposal = renderToStaticMarkup(
      <ChatBottomDock workflow={{ steps: [], proposedPlan: '## Release plan\n\n- Verify' }} />,
    );
    expect(proposal).toContain('aria-expanded="true"');
    expect(proposal).toContain('<h2>Release plan</h2>');

    const limited = renderToStaticMarkup(
      <ChatBottomDock workflow={{ goal: { objective: 'Keep working', status: 'budgetLimited' }, steps: [] }} />,
    );
    expect(limited).toContain('预算受限');
  });

  it('renders nothing without a workflow', () => {
    expect(renderToStaticMarkup(<ChatBottomDock />)).toBe('');
  });

  it('run 终态后不再渲染永远旋转的进行中：succeeded 补齐完成，failed 标记已中断', () => {
    const steps = [
      { step: '核对设计原语', status: 'completed' as const },
      { step: '实现工作流卡片', status: 'in_progress' as const },
      { step: '完成视觉验收', status: 'pending' as const },
    ];

    const succeeded = renderToStaticMarkup(
      <ChatBottomDock workflow={{ steps }} runStatus="succeeded" />,
    );
    expect(succeeded).toContain('3/3 已完成');
    expect(succeeded).not.toContain('进行中');
    expect(succeeded).not.toContain('aria-current="step"');

    const failed = renderToStaticMarkup(
      <ChatBottomDock workflow={{ steps }} runStatus="failed" />,
    );
    expect(failed).toContain('1/3 已完成');
    expect(failed).toContain('已中断');
    expect(failed).toContain('待处理');
    expect(failed).not.toContain('进行中');
  });
});
