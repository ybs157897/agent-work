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

  it('marks the root acceptance field invalid and disables submit when empty', () => {
    const html = renderToStaticMarkup(<CreateTaskModal open onClose={() => undefined} />);
    expect(html).toContain('根任务必填');
    expect(html).toContain('aria-required="true"');
    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain('根任务至少填写一条验收标准');
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>发布任务<\/button>/);
  });
});
