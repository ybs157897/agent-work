import { useId, type ReactNode } from 'react';
import { ExternalLink, type LucideIcon } from 'lucide-react';
import type { ContentBlock, ContentSource } from '../../../utils/content-blocks';

const DEFAULT_TITLES: Record<ContentBlock['type'], string> = {
  metric: '关键指标',
  table: '数据表',
  chart: '数据趋势',
  file: '文件',
  event: '日程',
  image: '图片',
  audio: '音频',
  map: '地点',
  search: '搜索结果',
  rating: '反馈',
  'review-summary': '评审结果',
  canvas: '流程画布',
};

export function ContentBlockShell({
  block,
  icon: Icon,
  children,
}: {
  block: ContentBlock;
  icon: LucideIcon;
  children: ReactNode;
}) {
  const headingId = useId();
  return (
    <section className="chat-content-block" data-content-block={block.type} aria-labelledby={headingId}>
      <header className="chat-content-block-head">
        <span className="chat-content-block-icon" aria-hidden><Icon className="h-4 w-4" /></span>
        <div className="min-w-0 flex-1">
          <h3 id={headingId} className="chat-content-block-title">{block.title ?? DEFAULT_TITLES[block.type]}</h3>
          {block.description && <p className="chat-content-block-description">{block.description}</p>}
        </div>
      </header>
      <div className="chat-content-block-body">{children}</div>
      {block.source && <ContentSourceLine source={block.source} />}
    </section>
  );
}

function ContentSourceLine({ source }: { source: ContentSource }) {
  return (
    <footer className="chat-content-block-source">
      <span>来源：{source.label}</span>
      {source.url && (
        <a href={source.url} target="_blank" rel="noreferrer noopener" aria-label={`打开来源：${source.label}`}>
          <ExternalLink className="h-3.5 w-3.5" aria-hidden />
        </a>
      )}
    </footer>
  );
}
