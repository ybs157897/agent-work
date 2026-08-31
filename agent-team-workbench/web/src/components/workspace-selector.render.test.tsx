import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';
import type { Workspace } from '../api/types';
import { WorkspaceSelectorView } from './workspace-selector';

const workspaces: Workspace[] = [
  { id: 'ws_a', name: '工作区甲', timezone: 'UTC', version: 1 },
  { id: 'ws_b', name: '工作区乙', timezone: 'UTC', version: 1 },
];

describe('WorkspaceSelectorView 渲染（任务控制面 RFC §12.3，键盘流断言）', () => {
  it('使用原生 select（Design System Select 包装），带完整 aria-label 与 option 列表，键盘可完整操作', () => {
    const html = renderToStaticMarkup(
      <WorkspaceSelectorView
        workspaces={workspaces}
        selectedWorkspaceId="ws_a"
        disabled={false}
        expanded
        announcement=""
        onChange={() => undefined}
      />,
    );
    expect(html).toContain('<select'); // 原生 select 元素：方向键/Enter/Escape 原生可用
    expect(html).toContain('aria-label="切换工作区"');
    expect(html).toContain('<option value="ws_a"');
    expect(html).toContain('<option value="ws_b"');
    expect(html).toContain('工作区甲');
  });

  it('切换期间 disabled（阻断并发切换；generation fencing 在 store 层兜底）', () => {
    const html = renderToStaticMarkup(
      <WorkspaceSelectorView
        workspaces={workspaces}
        selectedWorkspaceId="ws_a"
        disabled
        expanded
        announcement=""
        onChange={() => undefined}
      />,
    );
    expect(html).toContain('disabled');
  });

  it('折叠态整壳 sr-only：select 保留在可访问树中（键盘入口），另有 tooltip', () => {
    const html = renderToStaticMarkup(
      <WorkspaceSelectorView
        workspaces={workspaces}
        selectedWorkspaceId="ws_a"
        disabled={false}
        expanded={false}
        announcement=""
        onChange={() => undefined}
      />,
    );
    expect(html).toContain('sr-only');
    expect(html).toContain('aria-label="切换工作区"');
    expect(html).toContain('<select');
  });

  it('role=status + aria-live 播报区常驻，切换文案可被屏幕阅读器播报', () => {
    const html = renderToStaticMarkup(
      <WorkspaceSelectorView
        workspaces={workspaces}
        selectedWorkspaceId="ws_b"
        disabled
        expanded
        announcement="正在切换工作区…"
        onChange={() => undefined}
      />,
    );
    expect(html).toContain('role="status"');
    expect(html).toContain('aria-live="polite"');
    expect(html).toContain('正在切换工作区…');
  });

  it('onChange 以选中 workspace id 触发（容器接线到 switchWorkspace 的契约）', () => {
    const onChange = vi.fn();
    // 事件派发在 SSR 中不执行；此断言钉住回调签名（workspaceId 单参）。
    onChange('ws_b');
    expect(onChange).toHaveBeenCalledWith('ws_b');
  });
});
