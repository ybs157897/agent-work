import type { ReactNode } from 'react';
import { parseLanguageGuiFenceDocument } from '../../../utils/content-blocks';
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
  const document = parseLanguageGuiFenceDocument(source);
  return document ? <ContentBlockList document={document} trace={trace} /> : <>{fallback}</>;
}
