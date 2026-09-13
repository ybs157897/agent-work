import { FileWarning, Loader2 } from 'lucide-react';
import { useEffect, useState } from 'react';
import { ApiError } from '../../../api/client';
import { getRunFile } from '../../../api/endpoints';
import type { RunFileContent } from '../../../api/types';
import { captureScope, isCurrent } from '../../../stores/scope';
import { formatBytes } from '../../../utils/artifact-visuals';
import { Drawer } from '../../drawer';
import { MarkdownBody } from '../markdown-body';

type PreviewState =
  | { kind: 'loading' }
  | { kind: 'ready'; file: RunFileContent }
  | { kind: 'error'; message: string };

/** 把 problem+json 翻成用户能读懂的一句话；未知错误保留原文。 */
export function runFileErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'run_file_not_found':
        return '找不到该文件：它可能已被移动，或不在本次运行的仓库里。';
      case 'run_file_unsupported':
        return '该文件不是文本，或不是 UTF-8 内容，站内不预览。';
      case 'run_file_too_large':
        return '文件超过 1 MB 预览上限，请在本地打开。';
      case 'run_file_unavailable':
        return '这次运行的宿主没有站内预览通道，请在本地打开。';
      case 'invalid_file_path':
        return '这个路径不是仓库相对路径，无法打开。';
      default:
        return error.message;
    }
  }
  return error instanceof Error ? error.message : '文件打开失败';
}

/**
 * Run 文件只读预览抽屉：只传仓库相对路径，内容由控制平面在授权仓库集合内解析。
 * 内容按聊天同一条 Markdown 管线渲染（不可信文本，不执行 HTML/脚本）。
 */
export function RunFileDrawer({
  runId,
  path,
  onClose,
}: {
  runId: string | null;
  path: string | null;
  onClose: () => void;
}) {
  const [state, setState] = useState<PreviewState>({ kind: 'loading' });
  const open = runId !== null && path !== null;

  useEffect(() => {
    if (!runId || !path) return;
    const scope = captureScope();
    let cancelled = false;
    setState({ kind: 'loading' });
    getRunFile(runId, path)
      .then((file) => {
        if (cancelled || !isCurrent(scope)) return;
        setState({ kind: 'ready', file });
      })
      .catch((error: unknown) => {
        if (cancelled || !isCurrent(scope)) return;
        setState({ kind: 'error', message: runFileErrorMessage(error) });
      });
    return () => {
      cancelled = true;
    };
  }, [runId, path]);

  const title = state.kind === 'ready' ? state.file.name : (path ?? '').split('/').pop() ?? '文件预览';

  return (
    <Drawer open={open} onClose={onClose} title={title} ariaLabel={`文件预览：${path ?? ''}`} width={720}>
      <div className="chat-file-preview" data-run-file-preview={path ?? ''}>
        {state.kind === 'loading' && (
          <p className="chat-file-preview-status" role="status">
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden />正在读取 {path}…
          </p>
        )}
        {state.kind === 'error' && (
          <p className="chat-file-preview-status text-status-danger" role="alert">
            <FileWarning className="h-4 w-4" aria-hidden />
            {state.message}
          </p>
        )}
        {state.kind === 'ready' && (
          <>
            <div className="chat-file-preview-meta">
              <span className="chat-file-preview-path" title={state.file.path}>{state.file.path}</span>
              <span aria-hidden>·</span>
              <span className="tabular-nums">{formatBytes(state.file.size)}</span>
              <span aria-hidden>·</span>
              <span>{state.file.mime}</span>
            </div>
            {state.file.mime.startsWith('text/markdown') ? (
              <div className="chat-prose chat-file-preview-prose">
                <MarkdownBody text={state.file.content} streaming={false} />
              </div>
            ) : (
              <pre className="chat-file-preview-text">{state.file.content}</pre>
            )}
          </>
        )}
      </div>
    </Drawer>
  );
}
