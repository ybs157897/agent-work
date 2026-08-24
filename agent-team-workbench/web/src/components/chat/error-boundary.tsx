import { Component, type ErrorInfo, type ReactNode } from 'react';

interface MarkdownErrorBoundaryProps {
  /** 值变化时清除错误态重试子树：流式场景文本持续变化，一次失败不应钉死整段。 */
  resetKey: string;
  /** 子树抛错后的替代渲染。 */
  fallback: ReactNode;
  children: ReactNode;
}

interface MarkdownErrorBoundaryState {
  error: unknown | null;
}

/** markdown 树的容错壳（对齐 Codex 的 Markdown ErrorBoundary）：渲染崩溃回落 fallback，而不是炸掉整条消息。 */
export class MarkdownErrorBoundary extends Component<MarkdownErrorBoundaryProps, MarkdownErrorBoundaryState> {
  override state: MarkdownErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: unknown): MarkdownErrorBoundaryState {
    return { error };
  }

  override componentDidUpdate(prev: MarkdownErrorBoundaryProps): void {
    if (this.state.error !== null && prev.resetKey !== this.props.resetKey) {
      this.setState({ error: null });
    }
  }

  override componentDidCatch(error: unknown, info: ErrorInfo): void {
    // 渲染失败只记一次日志，不打扰用户界面
    console.error('chat markdown 渲染失败', error, info.componentStack);
  }

  override render(): ReactNode {
    return this.state.error === null ? this.props.children : this.props.fallback;
  }
}
