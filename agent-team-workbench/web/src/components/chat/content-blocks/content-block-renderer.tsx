import { lazy, Suspense, useEffect, useLayoutEffect, useRef } from 'react';
import type { ContentBlock, ContentBlockDocument } from '../../../utils/content-blocks';
import {
  isOutputTraceEnabled,
  outputTraceHash,
  stableOutputTraceJson,
  traceOutputDeduped,
  type OutputTraceMode,
} from '../../../utils/output-trace';
import { MarkdownErrorBoundary } from '../error-boundary';
import { EventBlock } from './event-block';
import { FileBlock } from './file-block';
import { MetricBlock } from './metric-block';
import { StructuredTableBlock } from './table-block';
import { AudioBlock, ImageBlock } from './media-blocks';
import { MapBlock } from './map-block';
import { SearchBlock } from './search-block';
import { RatingBlock } from './rating-block';
import { ReviewSummaryBlock } from './review-summary-block';

const LazyChartBlock = lazy(() => import('./chart-block').then((module) => ({ default: module.ChartBlock })));
const useCommitEffect = typeof window === 'undefined' ? useEffect : useLayoutEffect;

export interface ContentBlockTraceContext {
  runId?: string;
  messageId?: string;
  mode: OutputTraceMode;
}

export function ContentBlockList({
  document,
  trace,
}: {
  document: ContentBlockDocument;
  trace?: ContentBlockTraceContext;
}) {
  const lastCommitTrace = useRef('');
  useCommitEffect(() => {
    if (!isOutputTraceEnabled()) return;
    const serialized = stableOutputTraceJson(document);
    const hash = outputTraceHash(serialized);
    const signature = `content-block.committed:${trace?.runId ?? ''}:${trace?.messageId ?? ''}:${trace?.mode ?? 'final'}:${hash}`;
    if (lastCommitTrace.current === signature) return;
    lastCommitTrace.current = signature;
    traceOutputDeduped(signature, {
      stage: 'content-block.committed',
      mode: trace?.mode ?? 'final',
      source: 'react-commit',
      text: serialized,
      runId: trace?.runId,
      messageId: trace?.messageId,
      projection: {
        contentBlocks: document.blocks.length,
        blockTypes: document.blocks.map((block) => block.type),
        hash,
      },
      metadata: { version: document.version },
    });
  }, [document, trace?.messageId, trace?.mode, trace?.runId]);

  return (
    <div className="chat-content-block-list" data-content-block-version={document.version}>
      {document.blocks.map((block, index) => (
        <MarkdownErrorBoundary
          key={block.id ?? `${block.type}-${index}`}
          resetKey={`${block.id ?? index}-${block.type}`}
          fallback={<div className="chat-content-block-fallback" role="alert">此内容块暂时无法显示</div>}
        >
          <ContentBlockRenderer block={block} />
        </MarkdownErrorBoundary>
      ))}
    </div>
  );
}

export function ContentBlockRenderer({ block }: { block: ContentBlock }) {
  switch (block.type) {
    case 'metric':
      return <MetricBlock block={block} />;
    case 'table':
      return <StructuredTableBlock block={block} />;
    case 'chart':
      return (
        <Suspense fallback={<div className="chat-content-chart-loading" data-content-block="chart" role="status">正在加载图表…</div>}>
          <LazyChartBlock block={block} />
        </Suspense>
      );
    case 'file':
      return <FileBlock block={block} />;
    case 'event':
      return <EventBlock block={block} />;
    case 'image':
      return <ImageBlock block={block} />;
    case 'audio':
      return <AudioBlock block={block} />;
    case 'map':
      return <MapBlock block={block} />;
    case 'search':
      return <SearchBlock block={block} />;
    case 'rating':
      return <RatingBlock block={block} />;
    case 'review-summary':
      return <ReviewSummaryBlock block={block} />;
  }
}
