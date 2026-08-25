import { Compass } from 'lucide-react';
import { Link } from 'react-router-dom';
import { buttonClassName } from '../components/ui';

/** 404：品牌化兜底页。未知路由不再静默重定向，给用户明确的去向。 */
export default function NotFoundPage() {
  return (
    <main className="page-shell flex-1 flex flex-col">
      <div className="flex flex-1 flex-col items-center justify-center gap-comfortable text-center py-section">
        <div className="w-16 h-16 rounded-card bg-surface-sunken border border-border-subtle flex items-center justify-center">
          <Compass className="w-8 h-8 text-text-tertiary" strokeWidth={1.5} />
        </div>
        <div className="space-y-micro">
          <h1 className="text-h1 text-text-primary">该页面不存在</h1>
          <p className="text-body text-text-secondary max-w-md">
            你访问的地址不在工作台中。检查地址拼写，或回到总览继续。
          </p>
        </div>
        <Link to="/" className={buttonClassName('primary', 'md')}>
          返回总览
        </Link>
      </div>
    </main>
  );
}
