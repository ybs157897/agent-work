import { Headphones, Images } from 'lucide-react';
import type { AudioBlock as AudioBlockValue, ImageBlock as ImageBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

export function ImageBlock({ block }: { block: ImageBlockValue }) {
  return (
    <ContentBlockShell block={block} icon={Images}>
      <div className="chat-content-image-grid">
        {block.images.map((image, index) => (
          <figure key={`${index}-${image.src}`} className="chat-content-image">
            <img src={image.src} alt={image.alt} loading="lazy" />
            {image.caption && <figcaption>{image.caption}</figcaption>}
          </figure>
        ))}
      </div>
    </ContentBlockShell>
  );
}

export function AudioBlock({ block }: { block: AudioBlockValue }) {
  return (
    <ContentBlockShell block={block} icon={Headphones}>
      <ul className="chat-content-audio-list" aria-label="音频列表">
        {block.tracks.map((track, index) => (
          <li key={`${index}-${track.src}`} className="chat-content-audio-row">
            <div className="flex min-w-0 items-center justify-between gap-2">
              <span className="truncate font-medium text-text-primary">{track.title}</span>
              {track.duration && <span className="shrink-0 text-caption tabular-nums text-text-tertiary">{track.duration}</span>}
            </div>
            <audio controls preload="metadata" src={track.src} aria-label={`播放音频：${track.title}`} />
          </li>
        ))}
      </ul>
    </ContentBlockShell>
  );
}
