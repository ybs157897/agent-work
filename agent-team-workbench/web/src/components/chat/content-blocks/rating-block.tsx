import { useState } from 'react';
import { MessageSquareHeart, Star } from 'lucide-react';
import type { RatingBlock as RatingBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

export function RatingBlock({ block }: { block: RatingBlockValue }) {
  const [value, setValue] = useState(0);
  return (
    <ContentBlockShell block={block} icon={MessageSquareHeart}>
      <div className="chat-content-rating">
        <p className="text-body font-medium text-text-primary">{block.question}</p>
        <div className="chat-content-rating-scale">
          {block.lowLabel && <span>{block.lowLabel}</span>}
          <div className="chat-content-rating-buttons" role="radiogroup" aria-label={block.question}>
            {[1, 2, 3, 4, 5].map((score) => (
              <button key={score} type="button" role="radio" aria-checked={value === score} aria-label={`${score} 星`} onClick={() => setValue(score)} className={score <= value ? 'chat-content-rating-selected' : undefined}>
                <Star className="h-5 w-5" fill={score <= value ? 'currentColor' : 'none'} />
              </button>
            ))}
          </div>
          {block.highLabel && <span>{block.highLabel}</span>}
        </div>
        <p className="mt-tight text-caption text-text-tertiary">反馈仅保存在当前页面，不会自动上传。</p>
      </div>
    </ContentBlockShell>
  );
}
