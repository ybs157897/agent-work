import { useMemo } from 'react';
import { ChartNoAxesCombined } from 'lucide-react';
import { useReducedMotion } from 'motion/react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import type { ChartBlock as ChartBlockValue } from '../../../utils/content-blocks';
import { ContentBlockShell } from './content-block-shell';

const SERIES_COLORS = [
  'hsl(var(--color-brand-primary))',
  'hsl(var(--color-status-success))',
  'hsl(var(--color-status-warning))',
  'hsl(var(--color-text-secondary))',
];

export function ChartBlock({ block }: { block: ChartBlockValue }) {
  const reduceMotion = useReducedMotion();
  const data = useMemo(
    () => block.labels.map((label, index) => Object.fromEntries([
      ['label', label],
      ...block.series.map((series, seriesIndex) => [`series${seriesIndex}`, series.values[index]]),
    ])),
    [block.labels, block.series],
  );
  const summary = `${block.title ?? '数据趋势'}，${block.series.map((series) => `${series.name} 共 ${series.values.length} 个数据点`).join('；')}`;
  const allValues = block.series.flatMap((series) => series.values);
  const minValue = Math.min(...allValues);
  const maxValue = Math.max(...allValues);
  const autoPadding = Math.max((maxValue - minValue) * 0.12, Math.abs(maxValue) * 0.005, 1);
  const yDomain: [number | 'auto', number | 'auto'] = block.yDomain === 'auto'
    ? [minValue - autoPadding, maxValue + autoPadding]
    : [0, 'auto'];
  const common = (
    <>
      <CartesianGrid stroke="hsl(var(--color-border-subtle))" vertical={false} />
      <XAxis dataKey="label" tick={{ fill: 'hsl(var(--color-text-tertiary))', fontSize: 12 }} tickLine={false} axisLine={false} />
      <YAxis width="auto" domain={yDomain} tickFormatter={(value: number) => formatChartValue(value, block.unit)} tick={{ fill: 'hsl(var(--color-text-tertiary))', fontSize: 12 }} tickLine={false} axisLine={false} />
      <Tooltip
        contentStyle={{
          background: 'hsl(var(--color-surface-raised))',
          border: '1px solid hsl(var(--color-border-subtle))',
          borderRadius: 8,
          color: 'hsl(var(--color-text-primary))',
          boxShadow: 'var(--tx-shadow-card)',
        }}
        labelStyle={{ color: 'hsl(var(--color-text-secondary))' }}
      />
      {block.series.length > 1 && <Legend />}
    </>
  );

  return (
    <ContentBlockShell block={block} icon={ChartNoAxesCombined}>
      <div className="chat-content-chart" role="img" aria-label={summary}>
        <ResponsiveContainer width="100%" height={260} initialDimension={{ width: 720, height: 260 }}>
          {block.chart === 'bar' ? (
            <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} accessibilityLayer>
              {common}
              {block.series.map((series, index) => (
                <Bar key={series.name} dataKey={`series${index}`} name={series.name} unit={block.unit} fill={SERIES_COLORS[index]} radius={[6, 6, 0, 0]} isAnimationActive={!reduceMotion} />
              ))}
            </BarChart>
          ) : (
            <LineChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} accessibilityLayer>
              {common}
              {block.series.map((series, index) => (
                <Line key={series.name} dataKey={`series${index}`} name={series.name} unit={block.unit} stroke={SERIES_COLORS[index]} strokeWidth={2.5} dot={{ r: 3 }} activeDot={{ r: 5 }} isAnimationActive={!reduceMotion} />
              ))}
            </LineChart>
          )}
        </ResponsiveContainer>
      </div>
      <details className="chat-content-chart-data">
        <summary>查看数据表</summary>
        <div className="chat-content-table-scroll">
          <table className="chat-content-table">
            <thead><tr><th scope="col">标签</th>{block.series.map((series) => <th scope="col" key={series.name} className="text-right">{series.name}</th>)}</tr></thead>
            <tbody>{block.labels.map((label, index) => (
              <tr key={`${index}-${label}`}><th scope="row">{label}</th>{block.series.map((series) => <td key={series.name} className="text-right">{formatChartValue(series.values[index], block.unit)}</td>)}</tr>
            ))}</tbody>
          </table>
        </div>
      </details>
    </ContentBlockShell>
  );
}

function formatChartValue(value: number, unit = ''): string {
  const formatted = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value);
  return /^[€$£¥]$/.test(unit) ? `${unit}${formatted}` : `${formatted}${unit}`;
}
