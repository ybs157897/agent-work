import { Gauge } from 'lucide-react';
import type { MetricBlock as MetricBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

export function MetricBlock({ block }: { block: MetricBlockValue }) {
  return (
    <ContentBlockShell block={block} icon={Gauge}>
      <dl className="chat-content-metric-grid">
        {block.items.map((item, index) => (
          <div key={`${index}-${item.label}`} className="chat-content-metric-item">
            <dt>{item.label}</dt>
            <dd className="chat-content-metric-value">{item.value}</dd>
            {(item.delta || item.detail) && (
              <div className="chat-content-metric-meta">
                {item.delta && (
                  <span className={`chat-content-tone-${item.tone}`}>{item.delta}</span>
                )}
                {item.detail && <span>{item.detail}</span>}
              </div>
            )}
          </div>
        ))}
      </dl>
    </ContentBlockShell>
  );
}
