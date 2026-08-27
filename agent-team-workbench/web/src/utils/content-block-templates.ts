import {
  CONTENT_BLOCK_VERSION,
  type ContentBlockDocument,
  type ContentSource,
} from './content-blocks';

export function currencyTemplate(input: {
  title?: string;
  from: { code: string; name?: string; amount: string; rate?: string };
  to: { code: string; name?: string; amount: string; rate?: string };
  source?: ContentSource;
}): ContentBlockDocument {
  return document([{
    type: 'metric',
    title: input.title ?? '货币换算',
    source: input.source,
    items: [
      { label: `${input.from.code}${input.from.name ? ` · ${input.from.name}` : ''}`, value: input.from.amount, detail: input.from.rate, tone: 'neutral' },
      { label: `${input.to.code}${input.to.name ? ` · ${input.to.name}` : ''}`, value: input.to.amount, detail: input.to.rate, tone: 'neutral' },
    ],
  }]);
}

export function weatherTemplate(input: {
  location: string;
  temperature: string;
  condition: string;
  high?: string;
  low?: string;
  aqi?: string;
  uv?: string;
  hourly?: Array<{ time: string; temperature: string; condition: string }>;
  source?: ContentSource;
}): ContentBlockDocument {
  const blocks: ContentBlockDocument['blocks'] = [{
    type: 'metric',
    title: `${input.location}天气`,
    source: input.source,
    items: [
      { label: '当前温度', value: input.temperature, detail: input.condition, tone: 'neutral' },
      ...(input.high || input.low ? [{ label: '高 / 低', value: [input.high, input.low].filter(Boolean).join(' / '), tone: 'neutral' as const }] : []),
      ...(input.aqi ? [{ label: 'AQI', value: input.aqi, tone: 'neutral' as const }] : []),
      ...(input.uv ? [{ label: 'UV 指数', value: input.uv, tone: 'neutral' as const }] : []),
    ],
  }];
  if (input.hourly?.length) {
    blocks.push({
      type: 'table',
      title: '小时预报',
      columns: [
        { key: 'time', label: '时间', align: 'left' },
        { key: 'temperature', label: '温度', align: 'right' },
        { key: 'condition', label: '天气', align: 'left' },
      ],
      rows: input.hourly.map((item) => ({ ...item })),
    });
  }
  return document(blocks);
}

export function stockTemplate(input: {
  name: string;
  symbol: string;
  price: string;
  delta: string;
  open?: string;
  previousClose?: string;
  labels: string[];
  values: number[];
  unit?: string;
  source?: ContentSource;
}): ContentBlockDocument {
  return document([
    {
      type: 'metric',
      title: `${input.name} · ${input.symbol}`,
      source: input.source,
      items: [
        { label: '当前价格', value: input.price, delta: input.delta, tone: input.delta.trim().startsWith('-') ? 'negative' : 'positive' },
        ...(input.open ? [{ label: '开盘', value: input.open, tone: 'neutral' as const }] : []),
        ...(input.previousClose ? [{ label: '前收', value: input.previousClose, tone: 'neutral' as const }] : []),
      ],
    },
    {
      type: 'chart',
      chart: 'line',
      title: `${input.symbol} 走势`,
      labels: input.labels,
      series: [{ name: input.symbol, values: input.values }],
      unit: input.unit,
      yDomain: 'auto',
      source: input.source,
    },
  ]);
}

export function scoreTemplate(input: {
  league?: string;
  status: string;
  home: { name: string; score: string | number };
  away: { name: string; score: string | number };
  date?: string;
}): ContentBlockDocument {
  return document([{
    type: 'metric',
    title: input.league ? `${input.league} · ${input.status}` : input.status,
    description: input.date,
    items: [
      { label: input.home.name, value: String(input.home.score), tone: 'neutral' },
      { label: input.away.name, value: String(input.away.score), tone: 'neutral' },
    ],
  }]);
}

export function ratingTemplate(question: string): ContentBlockDocument {
  return document([{ type: 'rating', title: '回答反馈', question, lowLabel: '无帮助', highLabel: '很有帮助' }]);
}

export function mergeContentDocuments(...documents: ContentBlockDocument[]): ContentBlockDocument {
  return document(documents.flatMap((item) => item.blocks));
}

function document(blocks: ContentBlockDocument['blocks']): ContentBlockDocument {
  return { version: CONTENT_BLOCK_VERSION, blocks };
}
