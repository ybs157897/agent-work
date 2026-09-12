import { Table2 } from 'lucide-react';
import type { TableBlock as TableBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

const ALIGN_CLASS: Record<TableBlockValue['columns'][number]['align'], string> = {
  left: 'text-left',
  center: 'text-center',
  right: 'text-right',
};

export function StructuredTableBlock({ block }: { block: TableBlockValue }) {
  return (
    <ContentBlockShell block={block} icon={Table2}>
      <div className="chat-content-table-responsive">
        <table className="chat-content-table" role="table">
          <caption className="sr-only">{block.title ?? '数据表'}</caption>
          <thead role="rowgroup">
            <tr role="row">
              {block.columns.map((column) => (
                <th key={column.key} scope="col" role="columnheader" className={ALIGN_CLASS[column.align]}>{column.label}</th>
              ))}
            </tr>
          </thead>
          <tbody role="rowgroup">
            {block.rows.length > 0 ? block.rows.map((row, rowIndex) => (
              <tr key={rowIndex} role="row">
                {block.columns.map((column) => (
                  <td key={column.key} role="cell" className={ALIGN_CLASS[column.align]}>
                    <span className="chat-content-table-field" aria-hidden="true">{column.label}</span>
                    <span className="chat-content-table-value">{formatCell(row[column.key])}</span>
                  </td>
                ))}
              </tr>
            )) : (
              <tr role="row"><td role="cell" colSpan={block.columns.length} className="chat-content-table-empty">暂无记录</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </ContentBlockShell>
  );
}

function formatCell(value: string | number | boolean | null): string {
  if (value === null) return '—';
  if (typeof value === 'boolean') return value ? '是' : '否';
  return String(value);
}
