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
      <div className="chat-content-table-scroll">
        <table className="chat-content-table">
          <caption className="sr-only">{block.title ?? '数据表'}</caption>
          <thead>
            <tr>
              {block.columns.map((column) => (
                <th key={column.key} scope="col" className={ALIGN_CLASS[column.align]}>{column.label}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {block.rows.length > 0 ? block.rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                {block.columns.map((column) => (
                  <td key={column.key} className={ALIGN_CLASS[column.align]}>{formatCell(row[column.key])}</td>
                ))}
              </tr>
            )) : (
              <tr><td colSpan={block.columns.length} className="chat-content-table-empty">暂无记录</td></tr>
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
