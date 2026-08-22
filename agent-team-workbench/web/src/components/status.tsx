import type { AgentPresence } from '../api/types';

/** presence 是服务端运行投影（协议 §2.2）：idle/busy/degraded/offline。 */
export function PresenceDot({ presence, pulse = true }: { presence: AgentPresence; pulse?: boolean }) {
  switch (presence) {
    case 'busy':
      return <div className={`w-2 h-2 rounded-full bg-status-success shrink-0 ${pulse ? 'status-pulse' : ''}`} />;
    case 'degraded':
      return <div className="w-2 h-2 rounded-full bg-status-error shrink-0" />;
    case 'offline':
      return <div className="w-2 h-2 rounded-full bg-text-tertiary shrink-0" />;
    default:
      return <div className="w-2 h-2 rounded-full bg-status-standby shrink-0" />;
  }
}

export function presenceText(presence: AgentPresence): string {
  switch (presence) {
    case 'busy':
      return '运行中';
    case 'degraded':
      return '异常';
    case 'offline':
      return '离线';
    default:
      return '待命';
  }
}

const RUN_STATUS_TEXT: Record<string, string> = {
  queued: '排队中',
  starting: '启动中',
  running: '运行中',
  waiting_approval: '等待审批',
  interrupting: '中断中',
  cancelling: '取消中',
  reconnecting: '重连中',
  succeeding: '收尾中',
  succeeded: '已成功',
  interrupted: '已中断',
  cancelled: '已取消',
  lost: '已失联',
  failed: '已失败',
};

export function runStatusText(status: string): string {
  return RUN_STATUS_TEXT[status] ?? status;
}

export function runStatusColor(status: string): string {
  switch (status) {
    case 'succeeded':
      return 'text-status-success';
    case 'failed':
    case 'lost':
      return 'text-status-error';
    case 'cancelled':
    case 'interrupted':
      return 'text-text-tertiary';
    case 'waiting_approval':
      return 'text-status-warning';
    default:
      return 'text-brand-accent';
  }
}
