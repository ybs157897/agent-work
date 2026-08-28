import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ReasoningProcessPanel } from './reasoning-activity-row';

describe('ReasoningProcessPanel · 思考过程面板', () => {
  it('流式时渲染扫光且头行可折叠（不再强制展开锁死）', () => {
    const html = renderToStaticMarkup(
      <ReasoningProcessPanel panelKey="k1" text="先梳理需求再拆模块" streaming />,
    );

    // 流式扫光挂回正确作用域（老 .chat-reasoning-toggle 作用域已退役）。
    expect(html).toContain('chat-reasoning-sweep');
    expect(html).toContain('思考中…');
    // 头行按钮保留 aria-expanded 语义，不输出 disabled/aria-disabled——
    // 流式期间用户依旧可以折叠（回归：此前 onClick 直接 return，无法收起）。
    expect(html).toContain('aria-expanded="true"');
    expect(html).not.toContain('disabled');
    // 单 chevron：头行只有一枚展开箭头（回归：曾左右各一枚，方向语义互相矛盾）。
    expect(html.match(/lucide-chevron/g)?.length).toBe(1);
    // 展开体挂在 max-h 滚动面板内，长思考不撑爆正文流。
    expect(html).toContain('chat-reasoning-panel-body');
  });

  it('落定默认折叠时不渲染正文、aria-controls 不悬空', () => {
    const html = renderToStaticMarkup(
      <ReasoningProcessPanel panelKey="k2" text="已完成的思考" defaultExpanded={false} />,
    );

    expect(html).toContain('aria-expanded="false"');
    expect(html).not.toContain('chat-reasoning-panel-body');
    expect(html).not.toContain('aria-controls');
    expect(html).not.toContain('思考中…');
    // 不再展示编造的「持续了几秒」假时长。
    expect(html).not.toContain('持续了几秒');
  });

  it('无文本且非流式时不占位', () => {
    const html = renderToStaticMarkup(<ReasoningProcessPanel panelKey="k3" text="" />);
    expect(html).toBe('');
  });
});
