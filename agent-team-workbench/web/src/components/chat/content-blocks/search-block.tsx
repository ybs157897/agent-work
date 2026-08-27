import { ExternalLink, Search } from 'lucide-react';
import type { SearchBlock as SearchBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

export function SearchBlock({ block }: { block: SearchBlockValue }) {
  return (
    <ContentBlockShell block={block} icon={Search}>
      {block.query && <div className="chat-content-search-query">“{block.query}”</div>}
      <ol className="chat-content-search-results" aria-label="搜索结果">
        {block.results.map((result, index) => (
          <li key={`${index}-${result.url}`}>
            <div className="flex items-start gap-tight">
              <span className="chat-content-search-index" aria-hidden>{index + 1}</span>
              <div className="min-w-0 flex-1">
                <a href={result.url} target="_blank" rel="noreferrer noopener" className="chat-content-search-link">
                  <span>{result.title}</span><ExternalLink className="h-3.5 w-3.5 shrink-0" aria-hidden />
                </a>
                {result.source && <div className="mt-micro text-caption text-text-tertiary">{result.source}</div>}
                {result.snippet && <p className="mt-micro text-caption leading-5 text-text-secondary">{result.snippet}</p>}
              </div>
            </div>
          </li>
        ))}
      </ol>
    </ContentBlockShell>
  );
}
