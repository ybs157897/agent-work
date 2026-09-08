import { afterEach, describe, expect, it, vi } from 'vitest';
import { renderToStaticMarkup } from 'react-dom/server';
import { ApiError } from '../../api/client';
import {
  CodeWorkspace,
  codeWorkspaceErrorMessage,
  codeWorkspaceInput,
  createCodeWorkspaceReleaseGuard,
  installCodeWorkspacePageHide,
  readEmbeddedThemeTokens,
  sessionBindingLabel,
  shouldReleaseCodeWorkspaceResponse,
} from './code-workspace';

describe('code workspace view contract', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('sends conversation and run only when they exist', () => {
    expect(codeWorkspaceInput(null)).toEqual({});
    expect(codeWorkspaceInput('conversation_1')).toEqual({ conversation_id: 'conversation_1' });
    expect(codeWorkspaceInput('conversation_1', 'run_1')).toEqual({ conversation_id: 'conversation_1', run_id: 'run_1' });
  });

  it('turns missing-location errors into a setup instruction without exposing paths', () => {
    const error = new ApiError({
      type: 'about:blank',
      title: 'workspace location required',
      detail: '/Users/private/repository is missing',
      status: 422,
      code: 'workspace_location_required',
    });
    const message = codeWorkspaceErrorMessage(error);
    expect(message).toContain('配置代码仓库');
    expect(message).not.toContain('/Users/private');
  });

  it('waits for conversation history before claiming a default code directory', () => {
    const html = renderToStaticMarkup(<CodeWorkspace workspaceId="ws_1" agentId="agent_dev" conversationId="conversation_1" runsLoaded={false} theme="light" />);
    expect(html).toContain('正在读取当前会话的运行记录');
  });

  it('shows the repository and execution ref returned by the control plane', () => {
    expect(sessionBindingLabel({
      id: 'code_1', frame_url: '/view/', repository_identity: 'repo:demo',
      branch: 'codex/demo', worktree_ref: 'worktree:demo', read_only: true, expires_at: '',
    })).toBe('repo:demo · 分支 codex/demo · 工作树 worktree:demo');
  });

  it('releases a pagehide session once even when React cleanup follows', () => {
    const guard = createCodeWorkspaceReleaseGuard();
    const deleted: string[] = [];
    let pagehide: EventListener | undefined;
    let removed = 0;
    const target = {
      addEventListener: (_type: 'pagehide', listener: EventListener) => { pagehide = listener; },
      removeEventListener: (type: 'pagehide', listener: EventListener) => { void type; void listener; removed += 1; },
    } as Pick<Window, 'addEventListener' | 'removeEventListener'>;
    const remove = installCodeWorkspacePageHide(target, () => guard.release('code_1', (id) => deleted.push(id)));

    pagehide?.(new Event('pagehide'));
    guard.release('code_1', (id) => deleted.push(id));
    remove();

    expect(deleted).toEqual(['code_1']);
    expect(removed).toBe(1);
    expect(guard.hasReleased('code_1')).toBe(true);
  });

  it('releases a late create response after pagehide instead of publishing it', () => {
    expect(shouldReleaseCodeWorkspaceResponse(false, true, 1, 1)).toBe(true);
    expect(shouldReleaseCodeWorkspaceResponse(false, false, 1, 2)).toBe(true);
    expect(shouldReleaseCodeWorkspaceResponse(false, false, 1, 1)).toBe(false);
  });

  it('reads theme tokens from the scoped workbench theme, never the stale document root', () => {
    const scope = { dataset: { theme: 'dark' } };
    const frame = { closest: () => scope } as unknown as HTMLIFrameElement;
    vi.stubGlobal('window', { getComputedStyle: () => ({ getPropertyValue: (name: string) => name === '--color-surface-base' ? '222 28% 10%' : '' }) });
    vi.stubGlobal('document', { documentElement: { dataset: { theme: 'light' } } });

    expect(readEmbeddedThemeTokens(frame, 'dark')).toEqual({ '--color-surface-base': '222 28% 10%' });
    expect(readEmbeddedThemeTokens(frame, 'light')).toBeNull();
  });
});
