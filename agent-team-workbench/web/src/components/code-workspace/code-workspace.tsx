import { AlertCircle, Code2, LoaderCircle, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../../api/client';
import {
  createCodeWorkspace,
  deleteCodeWorkspace,
  type CodeWorkspaceSession,
} from '../../api/code-workspaces';
import type { WorkbenchTheme } from '../../stores/workbench-theme.store';
import { Button } from '../ui';
import './code-workspace.css';

export interface CodeWorkspaceProps {
  workspaceId: string;
  agentId: string;
  conversationId: string | null;
  /** True once the current conversation's run list has been read from the control plane. */
  runsLoaded: boolean;
  /** Used for the current workspace binding; a run status update never reopens the viewer. */
  latestRunId?: string;
  theme: WorkbenchTheme;
}

type CodeWorkspaceState =
  | { kind: 'opening' }
  | { kind: 'ready'; session: CodeWorkspaceSession; frameUrl: string }
  | { kind: 'error'; message: string };

function errorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 403 || error.code === 'agent_not_eligible') {
      return '当前 Agent 没有使用 Java 代码工作台的权限。';
    }
    if (
      error.status === 404
      || error.code === 'repository_not_found'
      || error.code === 'workspace_not_found'
      || error.code === 'workspace_location_required'
      || error.code === 'workspace_mount_not_advertised'
      || error.code === 'workspace_context_mismatch'
    ) {
      return '当前工作区还没有可用的代码目录。请先配置代码仓库，再重试。';
    }
    if (error.status >= 500 || error.retryable) {
      return '代码工作台暂时无法连接。请稍后重试。';
    }
  }
  if (error instanceof TypeError) return '代码工作台连接失败，请检查服务是否可用后重试。';
  return '代码工作台打开失败，请重试。';
}

function sameOriginFrameUrl(frameUrl: string): string | null {
  if (typeof window === 'undefined') return null;
  try {
    const parsed = new URL(frameUrl, window.location.href);
    return parsed.origin === window.location.origin ? parsed.toString() : null;
  } catch {
    return null;
  }
}

function codeWorkspaceInput(conversationId: string | null, runId?: string) {
  return {
    ...(conversationId ? { conversation_id: conversationId } : {}),
    ...(runId ? { run_id: runId } : {}),
  };
}

const EMBEDDED_THEME_TOKENS = [
  '--color-surface-base',
  '--color-surface-raised',
  '--color-surface-sunken',
  '--color-sidebar',
  '--color-sidebar-hover',
  '--color-sidebar-border',
  '--color-border-subtle',
  '--color-border-strong',
  '--color-text-primary',
  '--color-text-secondary',
  '--color-text-tertiary',
  '--color-brand-primary',
  '--color-status-error',
] as const;

export function readEmbeddedThemeTokens(frame: HTMLIFrameElement, theme: WorkbenchTheme): Record<string, string> | null {
  if (typeof window === 'undefined' || typeof document === 'undefined') return {};
  const scope = frame.closest<HTMLElement>('.workbench-theme') ?? document.documentElement;
  // LayoutShell writes data-theme in the same commit as the React theme. Do
  // not read documentElement's previous palette while that scoped attribute
  // is still catching up; the iframe load event will send the matching tokens.
  const scopedTheme = (scope as HTMLElement).dataset?.theme;
  if (scopedTheme && scopedTheme !== theme) return null;
  const styles = window.getComputedStyle(scope);
  return Object.fromEntries(
    EMBEDDED_THEME_TOKENS.flatMap((token) => {
      const value = styles.getPropertyValue(token).trim();
      return value ? [[token, value]] : [];
    }),
  );
}

function postThemeToFrame(frame: HTMLIFrameElement | null, frameUrl: string, theme: WorkbenchTheme): void {
  if (!frame?.contentWindow || typeof window === 'undefined') return;
  const targetOrigin = sameOriginFrameUrl(frameUrl);
  if (!targetOrigin) return;
  const tokens = readEmbeddedThemeTokens(frame, theme);
  if (!tokens) return;
  frame.contentWindow.postMessage({ type: 'atw-code-theme', theme, tokens }, targetOrigin);
}

function sessionBindingLabel(session: CodeWorkspaceSession): string {
  return [
    session.repository_identity,
    session.branch ? `分支 ${session.branch}` : '',
    session.worktree_ref ? `工作树 ${session.worktree_ref}` : '',
  ].filter(Boolean).join(' · ') || '当前工作区代码';
}

export interface CodeWorkspaceReleaseGuard {
  release(sessionId: string, release: (sessionId: string) => void): boolean;
  hasReleased(sessionId: string): boolean;
}

export function createCodeWorkspaceReleaseGuard(): CodeWorkspaceReleaseGuard {
  const released = new Set<string>();
  return {
    release(sessionId, release) {
      if (released.has(sessionId)) return false;
      released.add(sessionId);
      release(sessionId);
      return true;
    },
    hasReleased: (sessionId) => released.has(sessionId),
  };
}

export function shouldReleaseCodeWorkspaceResponse(
  disposed: boolean,
  pageHidden: boolean,
  requestVersion: number,
  currentVersion: number,
): boolean {
  return disposed || pageHidden || requestVersion !== currentVersion;
}

export function installCodeWorkspacePageHide(
  target: Pick<Window, 'addEventListener' | 'removeEventListener'> | undefined,
  listener: EventListener,
): () => void {
  if (!target) return () => {};
  target.addEventListener('pagehide', listener);
  return () => target.removeEventListener('pagehide', listener);
}

/**
 * Owns one backend code-workspace session for one workspace/Agent/conversation
 * scope. Run status changes keep the same ID and therefore keep the reader;
 * a newly discovered Run ID rebinds it to that Run's execution context.
 */
export function CodeWorkspace({ workspaceId, agentId, conversationId, runsLoaded, latestRunId, theme }: CodeWorkspaceProps) {
  const [retryNonce, setRetryNonce] = useState(0);
  const [state, setState] = useState<CodeWorkspaceState>({ kind: 'opening' });
  const iframeRef = useRef<HTMLIFrameElement>(null);
  const requestVersion = useRef(0);
  const activeSessionRef = useRef<CodeWorkspaceSession | null>(null);
  const originWorkspaceRef = useRef<string | null>(null);
  const [releaseGuard] = useState<CodeWorkspaceReleaseGuard>(() => createCodeWorkspaceReleaseGuard());

  const releaseSession = useCallback((sessionId: string, onFailure?: (error: unknown) => void) => {
    releaseGuard.release(sessionId, (releasedId) => {
      void deleteCodeWorkspace(releasedId, true, originWorkspaceRef.current ?? workspaceId).catch((error: unknown) => {
        if (onFailure) onFailure(error);
        else console.error('code workspace cleanup failed', error);
      });
    });
  }, [releaseGuard, workspaceId]);

  useEffect(() => {
    let disposed = false;
    let pageHidden = false;
    const version = ++requestVersion.current;
    originWorkspaceRef.current = workspaceId;
    let created: CodeWorkspaceSession | null = null;
    const onPageHide: EventListener = () => {
      pageHidden = true;
      const session = created ?? activeSessionRef.current;
      if (!session) return;
      if (activeSessionRef.current?.id === session.id) activeSessionRef.current = null;
      releaseSession(session.id);
    };
    const removePageHide = installCodeWorkspacePageHide(
      typeof window === 'undefined' ? undefined : window,
      onPageHide,
    );

    setState({ kind: 'opening' });
    if (!runsLoaded) {
      return () => {
        removePageHide();
        disposed = true;
        if (created) {
          const session = created;
          created = null;
          if (activeSessionRef.current?.id === session.id) activeSessionRef.current = null;
          releaseSession(session.id);
        }
      };
    }
    void createCodeWorkspace(
      workspaceId,
      agentId,
      codeWorkspaceInput(conversationId, latestRunId),
    ).then((session) => {
      if (shouldReleaseCodeWorkspaceResponse(disposed, pageHidden, version, requestVersion.current)) {
        releaseSession(session.id);
        return;
      }
      const frameUrl = sameOriginFrameUrl(session.frame_url);
      if (!frameUrl) {
        releaseSession(session.id);
        setState({ kind: 'error', message: '代码工作台返回了无效的嵌入地址，请重试。' });
        return;
      }
      created = session;
      activeSessionRef.current = session;
      setState({ kind: 'ready', session, frameUrl });
    }).catch((error: unknown) => {
      if (disposed || version !== requestVersion.current) return;
      setState({ kind: 'error', message: errorMessage(error) });
    });

    return () => {
      removePageHide();
      disposed = true;
      if (created) {
        const session = created;
        created = null;
        if (activeSessionRef.current?.id === session.id) activeSessionRef.current = null;
        releaseSession(session.id);
      }
    };
  }, [agentId, conversationId, latestRunId, releaseSession, retryNonce, runsLoaded, workspaceId]);

  useEffect(() => {
    if (state.kind !== 'ready') return;
    const expiresAt = Date.parse(state.session.expires_at);
    if (!Number.isFinite(expiresAt)) return;
    const expire = () => {
      if (activeSessionRef.current?.id === state.session.id) activeSessionRef.current = null;
      setState({ kind: 'error', message: '代码阅读会话已到期，请重新连接。' });
      releaseSession(state.session.id, () => {
        setState((current) => current.kind === 'error'
          ? { kind: 'error', message: '代码阅读会话已到期，旧会话释放失败，请重试。' }
          : current);
      });
    };
    const delay = Math.max(0, expiresAt - Date.now());
    if (delay === 0) {
      expire();
      return;
    }
    const timer = window.setTimeout(expire, delay);
    return () => window.clearTimeout(timer);
  }, [releaseSession, state]);

  const sendTheme = useCallback(() => {
    if (state.kind !== 'ready') return;
    postThemeToFrame(iframeRef.current, state.frameUrl, theme);
  }, [state, theme]);

  useEffect(() => {
    sendTheme();
  }, [sendTheme]);

  return (
    <section className="code-workspace-shell" aria-label="Java 代码工作台">
      <header className="code-workspace-header">
        <div className="flex min-w-0 items-center gap-tight">
          <span className="code-workspace-mark" aria-hidden><Code2 className="h-4 w-4" /></span>
          <div className="min-w-0">
            <h2 className="truncate text-body font-semibold text-text-primary">Java 代码工作台</h2>
            <p className="truncate text-caption text-text-tertiary" title={state.kind === 'ready' ? sessionBindingLabel(state.session) : undefined}>
              {state.kind === 'ready' ? sessionBindingLabel(state.session) : '只读查看 · 与当前开发 Agent 会话绑定'}
            </p>
          </div>
        </div>
        {state.kind === 'ready' && <div className="flex shrink-0 items-center gap-tight">
          <span className="code-workspace-readonly" role="status">
            <span className="h-1.5 w-1.5 rounded-full bg-status-success" aria-hidden />只读
          </span>
          <button type="button" className="code-workspace-reconnect" onClick={() => { setState({ kind: 'opening' }); setRetryNonce((value) => value + 1); }} aria-label="重新连接代码工作台">
            <RefreshCw className="h-3.5 w-3.5" aria-hidden />重新连接
          </button>
        </div>}
      </header>

      {state.kind === 'opening' && (
        <div className="code-workspace-state" role="status" aria-live="polite">
          <LoaderCircle className="h-5 w-5 animate-spin text-brand-primary" aria-hidden />
          <div>
            <p className="text-body font-medium text-text-primary">正在打开代码工作台</p>
            <p className="mt-1 text-caption text-text-tertiary">{runsLoaded ? '正在绑定当前工作区的代码目录…' : '正在读取当前会话的运行记录…'}</p>
          </div>
        </div>
      )}

      {state.kind === 'error' && (
        <div className="code-workspace-state" role="alert">
          <AlertCircle className="h-5 w-5 shrink-0 text-status-error" aria-hidden />
          <div className="min-w-0">
            <p className="text-body font-medium text-text-primary">代码工作台不可用</p>
            <p className="mt-1 text-caption leading-5 text-text-secondary">{state.message}</p>
            <Button type="button" size="sm" className="mt-3" onClick={() => setRetryNonce((value) => value + 1)}>
              <RefreshCw className="h-3.5 w-3.5" aria-hidden />重试
            </Button>
          </div>
        </div>
      )}

      {state.kind === 'ready' && (
        <div className="code-workspace-frame">
          <iframe
            ref={iframeRef}
            src={state.frameUrl}
            title="Java 代码工作台（只读）"
            referrerPolicy="no-referrer"
            onLoad={sendTheme}
            onError={() => setState({ kind: 'error', message: '代码工作台页面加载失败，请重试。' })}
          />
        </div>
      )}
    </section>
  );
}

export { codeWorkspaceInput, errorMessage as codeWorkspaceErrorMessage, sameOriginFrameUrl, sessionBindingLabel };
