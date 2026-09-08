import { Link } from 'react-router-dom';

export function SendErrorNotice({ message, agentId }: { message: string; agentId: string | null }) {
  return (
    <section role="alert" className="rounded-card border border-status-error/30 bg-status-error/5 px-snug py-tight text-body">
      <p className="text-status-error">消息暂未发送，原文已保留</p>
      <p className="mt-tight text-text-secondary">检查配置后，可以继续发送待发消息，或重新发送输入框中的内容。</p>
      <div className="mt-tight flex flex-wrap gap-base text-caption">
        <Link to={agentId ? `/agents?agent=${encodeURIComponent(agentId)}` : '/agents'} className="text-brand-primary underline focus-visible:ring-2 focus-visible:ring-brand-primary/40">检查智能体配置</Link>
        <Link to="/settings" className="text-brand-primary underline focus-visible:ring-2 focus-visible:ring-brand-primary/40">检查连接设置</Link>
      </div>
      <details className="mt-tight text-caption text-text-secondary">
        <summary className="cursor-pointer focus-visible:ring-2 focus-visible:ring-brand-primary/40">查看失败原因</summary>
        <p className="mt-tight whitespace-pre-wrap break-words">{message}</p>
      </details>
    </section>
  );
}
