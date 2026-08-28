import type { ReactNode } from 'react';
import { parseContentBlockDocument } from '../../../utils/content-blocks';
import { ContentBlockList, type ContentBlockTraceContext } from './content-block-renderer';

export function LanguageGuiFence({
  source,
  fallback,
  trace,
}: {
  source: string;
  fallback: ReactNode;
  trace?: ContentBlockTraceContext;
}) {
  const document = parseContentBlockDocument(source);
  return document ? <ContentBlockList document={document} trace={trace} /> : <>{fallback}</>;
}
