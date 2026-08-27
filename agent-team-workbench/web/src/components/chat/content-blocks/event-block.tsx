import { CalendarDays, Clock3, ExternalLink, MapPin } from 'lucide-react';
import type { EventBlock as EventBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

const MONTHS = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC'];

export function EventBlock({ block }: { block: EventBlockValue }) {
  const date = calendarParts(block.start);
  const sameDay = block.end ? block.start.slice(0, 10) === block.end.slice(0, 10) : false;
  return (
    <ContentBlockShell block={block} icon={CalendarDays}>
      <article className="chat-content-event">
        <div className="chat-content-event-date" aria-label={date.label}>
          <span>{date.month}</span>
          <strong>{date.day}</strong>
        </div>
        <div className="min-w-0 flex-1">
          <div className="chat-content-event-line">
            <Clock3 className="h-3.5 w-3.5" aria-hidden />
            <time dateTime={block.start}>{formatEventTime(block.start, false)}</time>
            {block.end && <><span aria-hidden>–</span><time dateTime={block.end}>{formatEventTime(block.end, sameDay)}</time></>}
            {block.timezone && <span className="chat-content-badge">{block.timezone}</span>}
          </div>
          {block.location && (
            <div className="chat-content-event-line"><MapPin className="h-3.5 w-3.5" aria-hidden />{block.location}</div>
          )}
        </div>
        {block.url && (
          <a href={block.url} target="_blank" rel="noreferrer noopener" className="chat-content-event-action" aria-label={`查看事件：${block.title}`}>
            查看<ExternalLink className="h-3.5 w-3.5" aria-hidden />
          </a>
        )}
      </article>
    </ContentBlockShell>
  );
}

function calendarParts(value: string): { month: string; day: string; label: string } {
  const match = value.match(/^(\d{4})-(\d{2})-(\d{2})/);
  if (!match) return { month: 'DATE', day: '—', label: value };
  const month = Number(match[2]);
  return {
    month: MONTHS[month - 1] ?? 'DATE',
    day: String(Number(match[3])),
    label: `${match[1]} 年 ${month} 月 ${Number(match[3])} 日`,
  };
}

function formatEventTime(value: string, timeOnly: boolean): string {
  const dateOnly = /^\d{4}-\d{2}-\d{2}$/.test(value);
  const parsed = new Date(value);
  if (dateOnly || Number.isNaN(parsed.getTime())) return value;
  return new Intl.DateTimeFormat('zh-CN', {
    ...(timeOnly ? {} : { month: 'short', day: 'numeric' }),
    hour: '2-digit',
    minute: '2-digit',
  }).format(parsed);
}
