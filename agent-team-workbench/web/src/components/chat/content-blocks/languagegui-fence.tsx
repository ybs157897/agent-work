import type { ReactNode } from 'react';
import { parseContentBlockDocument } from '../../../utils/content-blocks';
import { ContentBlockList } from './content-block-renderer';

export function LanguageGuiFence({ source, fallback }: { source: string; fallback: ReactNode }) {
  const document = parseContentBlockDocument(source);
  return document ? <ContentBlockList document={document} /> : <>{fallback}</>;
}
