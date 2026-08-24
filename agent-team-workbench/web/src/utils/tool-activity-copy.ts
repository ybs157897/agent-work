import type { ToolFamily, ToolRowState } from '../components/chat/tool-model';

export interface ToolActivityCopyInput {
  family: ToolFamily;
  state: ToolRowState;
  summary: string;
  filePath?: string;
}

function target(input: ToolActivityCopyInput): string {
  const t = input.filePath?.trim() || input.summary.trim();
  return t !== '' ? t : '…';
}

/** Codex 风格动宾三态标题（进行时 / 完成 / 失败）。 */
export function toolActivityTitle(input: ToolActivityCopyInput): string {
  const { family, state } = input;
  const tgt = target(input);

  if (state === 'running') {
    switch (family) {
      case 'bash':
        return input.summary.trim() ? `Running ${input.summary.trim()}` : 'Running command';
      case 'read':
        return `Reading ${tgt}`;
      case 'write':
      case 'edit':
        return `Updating ${tgt}`;
      case 'search':
        return input.summary.trim() ? `Searching ${input.summary.trim()}` : 'Searching…';
      case 'code':
        return 'Running code';
      default:
        return 'Running tool';
    }
  }

  if (state === 'error' || state === 'stopped') {
    switch (family) {
      case 'bash':
        return state === 'stopped' ? 'Stopped command' : 'Command failed';
      case 'read':
        return "Couldn't read file";
      case 'write':
      case 'edit':
        return "Couldn't update file";
      case 'search':
        return 'Search failed';
      case 'code':
        return 'Code run failed';
      default:
        return 'Tool failed';
    }
  }

  switch (family) {
    case 'bash':
      return 'Ran command';
    case 'read':
      return `Read ${tgt}`;
    case 'write':
    case 'edit':
      return `Updated ${tgt}`;
    case 'search':
      return 'Searched';
    case 'code':
      return 'Ran code';
    default:
      return 'Tool call';
  }
}

/** 活动组头滚动摘要：running 工具摘要优先，否则 reasoning 末行。 */
export function activityGroupHeadSummary(
  toolSummaries: readonly string[],
  reasoningPreview: string | undefined,
  running: boolean,
): string {
  const runningSummary = toolSummaries.find((s) => s.trim() !== '');
  if (running && runningSummary) return runningSummary;
  if (reasoningPreview?.trim()) return reasoningPreview.trim();
  if (toolSummaries.length > 0) return `${toolSummaries.length} 次调用`;
  return '活动';
}
