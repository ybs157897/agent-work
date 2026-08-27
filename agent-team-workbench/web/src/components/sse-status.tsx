import { StatusPill } from './ui';
import { useWorkspaceStore } from '../stores/workspace.store';

const SSE_LABEL = { online: '实时连接', connecting: '连接中', reconnecting: '重连中' } as const;
const SSE_DOT = {
  online: 'bg-status-success status-pulse',
  connecting: 'bg-status-standby',
  reconnecting: 'bg-status-warning status-pulse',
} as const;

/** SSE 连接胶囊：壳层顶栏与对话页头共用，避免 `/chat` 收起宣纸顶栏后丢失连接态。 */
export function SseStatusPill() {
  const sseStatus = useWorkspaceStore((state) => state.sseStatus);
  return (
    <StatusPill title="SSE 实时事件连接状态">
      <span className={`h-2 w-2 rounded-full ${SSE_DOT[sseStatus]}`} />
      <span>{SSE_LABEL[sseStatus]}</span>
    </StatusPill>
  );
}
