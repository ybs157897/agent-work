import { ExternalLink, MapPin, MapPinned } from 'lucide-react';
import type { MapBlock as MapBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

export function MapBlock({ block }: { block: MapBlockValue }) {
  const coordinates = block.latitude !== undefined && block.longitude !== undefined
    ? `${block.latitude.toFixed(4)}, ${block.longitude.toFixed(4)}`
    : '';
  return (
    <ContentBlockShell block={block} icon={MapPinned}>
      {block.imageUrl && <img src={block.imageUrl} alt={`地图：${block.location}`} loading="lazy" className="chat-content-map-image" />}
      <div className="chat-content-map-summary">
        <span className="chat-content-map-marker" aria-hidden><MapPin className="h-4 w-4" /></span>
        <div className="min-w-0 flex-1">
          <div className="font-medium text-text-primary">{block.location}</div>
          {coordinates && <code className="mt-micro block text-caption text-text-tertiary">{coordinates}</code>}
        </div>
        {block.url && (
          <a href={block.url} target="_blank" rel="noreferrer noopener" className="chat-content-event-action" aria-label={`在地图中打开：${block.location}`}>
            打开地图<ExternalLink className="h-3.5 w-3.5" aria-hidden />
          </a>
        )}
      </div>
    </ContentBlockShell>
  );
}
