import type { ReactNode } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useWorkspaceStore } from '../../stores/workspace.store';
import { CreateTaskModal } from './create-task-modal';

vi.mock('../../components/drawer', () => ({
  Drawer: ({ open, title, children }: { open: boolean; title?: string; children: ReactNode }) =>
    open ? <div role="dialog" aria-label={title}>{children}</div> : null,
}));

describe('CreateTaskModal root acceptance contract', () => {
  beforeEach(() => {
    useWorkspaceStore.setState({
      workspace: { id: 'ws_1', name: 'workspace', timezone: 'UTC', version: 1 },
    });
  });

  it('初次打开提示必填但不提前报错，未填写完整时禁止发布', () => {
    const html = renderToStaticMarkup(<CreateTaskModal open onClose={() => undefined} />);
    expect(html).toContain('必填，每行一条');
    expect(html).toContain('aria-required="true"');
    expect(html).not.toContain('aria-invalid="true"');
    expect(html).not.toContain('role="alert"');
    expect(html).toContain('写清满足什么条件才算完成');
    expect(html).not.toContain('父任务');
    expect(html).not.toContain('子任务');
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>发布任务<\/button>/);
  });
});
