/**
 * LanguageGUI 演示页（支线 zcode/languagegui-demo）· 真模型版。
 * 复刻 Tonki Labs「LanguageGUI — A UI Kit for LLMs」（MIT）的设计语言，并接通
 * 工作台配置里的真实模型：kimi（K2.7/K3）与 deepseek（V4 Flash/Pro），经本地
 * 零依赖代理 scripts/languagegui-proxy.mjs 流式转发 openai-completions。
 *
 * 部件协议（LanguageGUI 官网 FAQ 的「LLM 结构化输出→widget 实时渲染」思路）：
 * 模型输出 文本 + ```lgui JSON 块，前端增量解析成 widget 卡——见
 * languagegui-segments.ts。独立路由 /languagegui，不进工作台壳层与语义 token。
 */

import {
  ArrowLeftRight,
  BarChart3,
  Bookmark,
  Check,
  ChevronDown,
  Clock3,
  Copy,
  Gauge,
  MessageSquare,
  Mic,
  MoreHorizontal,
  Navigation,
  Paperclip,
  Pencil,
  Plus,
  RefreshCw,
  Settings,
  Share2,
  Star,
  ThumbsDown,
  ThumbsUp,
} from 'lucide-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import {
  currencyFlag,
  parseClockTime,
  parseSegments,
  SYSTEM_PROMPT,
  type LguiWidgetData,
} from './languagegui-segments';
import { MarkdownBody } from '../components/chat/markdown-body';
import { copyText } from '../components/chat/code-block';
import shanShuiPanorama from '../assets/ink/shan-shui-panorama.webp';
import { currencyTemplate, mergeContentDocuments, ratingTemplate, scoreTemplate, stockTemplate, weatherTemplate } from '../utils/content-block-templates';
import { LanguageGuiToolShowcase } from './languagegui-tool-showcase';
import './languagegui-demo.css';

const API = '/languagegui-api';
const DEFAULT_MODEL = 'deepseek-v4-flash';

interface ChatMessage {
  role: 'user' | 'assistant';
  text: string;
  /** assistant：生成该回复的模型 ref（回合头展示，切换模型后不失真）。 */
  model?: string;
  /** assistant：思考模型的 reasoning 内容，折叠展示。 */
  reasoning?: string;
}

interface ModelOption {
  ref: string;
  display: string;
  provider: string;
}

/* ── widget 渲染器（数据驱动） ─────────────────────────────── */

function ClockFace({ hour, minute, dim = false }: { hour: number; minute: number; dim?: boolean }) {
  const hourAngle = ((hour % 12) + minute / 60) * 30;
  const minuteAngle = minute * 6;
  return (
    <svg className={`lgui-clock-face${dim ? ' lgui-clock-dim' : ''}`} width="64" height="64" viewBox="0 0 64 64" aria-hidden>
      <circle cx="32" cy="32" r="29" fill="var(--lg-surface)" stroke="var(--lg-border)" strokeWidth="2" />
      {Array.from({ length: 12 }, (_, i) => (
        <line key={i} x1="32" y1="8" x2="32" y2="11" stroke="var(--lg-border-active)" strokeWidth="1.5" strokeLinecap="round" transform={`rotate(${i * 30} 32 32)`} />
      ))}
      <line x1="32" y1="32" x2="32" y2="19" stroke="var(--lg-ink)" strokeWidth="3" strokeLinecap="round" transform={`rotate(${hourAngle} 32 32)`} />
      <line x1="32" y1="32" x2="32" y2="13" stroke="var(--lg-accent)" strokeWidth="2" strokeLinecap="round" transform={`rotate(${minuteAngle} 32 32)`} />
      <circle cx="32" cy="32" r="2.5" fill="var(--lg-ink)" />
    </svg>
  );
}

interface ClockCity {
  city?: string;
  tz?: string;
  time?: string;
  date?: string;
  active?: boolean;
}

function ClockWidget({ cities }: { cities: ClockCity[] }) {
  return (
    <div className="lgui-widget-stack">
      {cities.map((c, i) => {
        const { hour, minute } = parseClockTime(c.time || '');
        return (
          <div key={i} className={`lgui-widget lgui-clock-row${c.active ? ' lgui-widget-active' : ''}`}>
            <div className="lgui-clock-city">
              <b>{c.city || '—'}</b>
              <span className="lgui-chip">{c.tz || ''}</span>
            </div>
            <div className={`lgui-clock-time${c.active ? '' : ' lgui-clock-dim'}`}>
              <b>{c.time || '—'}</b>
              <span>{c.date || ''}</span>
            </div>
            <ClockFace hour={hour} minute={minute} dim={!c.active} />
          </div>
        );
      })}
    </div>
  );
}

function FxWidget({ data }: { data: LguiWidgetData }) {
  const [swapped, setSwapped] = useState(false);
  const str = (k: string) => (typeof data[k] === 'string' ? (data[k] as string) : '');
  const from = swapped ? str('to') : str('from');
  const to = swapped ? str('from') : str('to');
  const result = swapped ? str('amount') : str('result');
  return (
    <div className="lgui-widget">
      <span className="lgui-amount-label">Amount · {from}</span>
      <div className="lgui-amount">{str('amount')}</div>
      <div className="lgui-fx-row">
        <span className="lgui-fx-flag">{currencyFlag(from)}</span>
        <span className="lgui-fx-code">
          <b>{from}</b>
          <span>{str('rate')}</span>
        </span>
        <span className="lgui-fx-value">{swapped ? str('result') : str('amount')}</span>
      </div>
      <div className="lgui-fx-swap">
        <button type="button" aria-label="交换币种" title="交换币种" onClick={() => setSwapped((value) => !value)}>
          <ArrowLeftRight size={15} />
        </button>
      </div>
      <div className="lgui-fx-row">
        <span className="lgui-fx-flag">{currencyFlag(to)}</span>
        <span className="lgui-fx-code">
          <b>{to}</b>
          <span>{str('rate')}</span>
        </span>
        <span className="lgui-fx-value">{result}</span>
      </div>
    </div>
  );
}

type TableCell = string | number | { text?: string; tone?: string };

function tableCellText(cell: TableCell): string {
  return typeof cell === 'object' && cell !== null ? String(cell.text ?? '') : String(cell);
}

function isNumericTableValue(value: string): boolean {
  return /^\s*[+$€£¥]?\s*[+-]?(?:\d[\d,]*(?:\.\d+)?|\.\d+)\s*(?:%|pp|[kKmMbB万亿千百十]*)?\s*$/i.test(value);
}

function TableWidget({ data }: { data: LguiWidgetData }) {
  const columns = Array.isArray(data.columns) ? (data.columns as unknown[]) : [];
  const rows = Array.isArray(data.rows) ? (data.rows as unknown[]) : [];
  const numericColumns = columns.map((_, columnIndex) => {
    const values = rows
      .map((row) => (Array.isArray(row) ? row[columnIndex] : undefined))
      .filter((cell): cell is TableCell => cell !== undefined && tableCellText(cell).trim() !== '')
      .map(tableCellText);
    return values.length > 0 && values.every(isNumericTableValue);
  });
  return (
    <div className="lgui-widget">
      {typeof data.title === 'string' && data.title ? (
        <div className="lgui-widget-title">
          <h4>{data.title}</h4>
        </div>
      ) : null}
      <table className="lgui-table">
        {columns.length > 0 ? (
          <thead>
            <tr>
              {columns.map((c, i) => (
                <th key={i} className={numericColumns[i] ? 'lgui-num' : undefined}>{String(c)}</th>
              ))}
            </tr>
          </thead>
        ) : null}
        <tbody>
          {rows.map((row, r) => (
            <tr key={r}>
              {(Array.isArray(row) ? row : [row]).map((cell: TableCell, c) => {
                const obj = typeof cell === 'object' && cell !== null ? cell : null;
                const tone = obj && typeof obj.tone === 'string' ? obj.tone : '';
                const text = tableCellText(cell);
                const cls = ['lgui-table-cell', numericColumns[c] ? 'lgui-num' : '', tone === 'up' ? 'lgui-delta-up' : tone === 'down' ? 'lgui-delta-down' : ''].filter(Boolean).join(' ');
                return (
                  <td key={c} className={cls}>{text}</td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ChartWidget({ data }: { data: LguiWidgetData }) {
  const labels = Array.isArray(data.labels) ? (data.labels as unknown[]).map(String) : [];
  const values = Array.isArray(data.values) ? (data.values as unknown[]).map((v) => Number(v) || 0) : [];
  const max = Math.max(...values, 1);
  const unit = typeof data.unit === 'string' ? data.unit : '';
  return (
    <div className="lgui-widget">
      {typeof data.title === 'string' && data.title ? (
        <div className="lgui-widget-title">
          <h4>{data.title}</h4>
        </div>
      ) : null}
      <div className="lgui-chart">
        {values.map((v, i) => (
          <div key={i} className="lgui-chart-col">
            <span className="lgui-chart-value">{v}{unit}</span>
            <i className={v >= max * 0.85 ? 'lgui-spark-hi' : undefined} style={{ height: `${Math.max(6, (v / max) * 100)}px` }} />
            <span className="lgui-chart-label">{labels[i] ?? ''}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function RatingWidget({ question }: { question: string }) {
  const [rating, setRating] = useState(0);
  return (
    <div className="lgui-widget lgui-rating">
      <div className="lgui-rating-text">
        <b>{question}</b>
        <span>演示件：反馈不上传</span>
      </div>
      <div className="lgui-stars" role="radiogroup" aria-label="评分">
        {[1, 2, 3, 4, 5].map((i) => (
          <button key={i} type="button" className="lgui-star" role="radio" aria-label={`${i} 星`} aria-checked={rating === i} onClick={() => setRating(i)}>
            <Star size={22} fill={i <= rating ? 'currentColor' : 'none'} />
          </button>
        ))}
      </div>
    </div>
  );
}

function WidgetRenderer({ data }: { data: LguiWidgetData }) {
  switch (data.widget) {
    case 'clock':
      return Array.isArray(data.cities) ? <ClockWidget cities={data.cities as ClockCity[]} /> : null;
    case 'fx':
      return <FxWidget data={data} />;
    case 'table':
      return <TableWidget data={data} />;
    case 'chart':
      return <ChartWidget data={data} />;
    case 'rating':
      return <RatingWidget question={typeof data.question === 'string' ? data.question : '这次回答对你有帮助吗？'} />;
    default:
      return (
        <div className="lgui-widget">
          <div className="lgui-widget-title"><h4>未知部件 · {String(data.widget)}</h4></div>
          <pre className="lgui-broken">{JSON.stringify(data, null, 2)}</pre>
        </div>
      );
  }
}

/* ── 消息渲染 ──────────────────────────────────────────────── */

/** 思考块：思考模型先吐的 reasoning 折叠展示。流式中默认展开，落定自动收起，可手动重开。 */
function ReasoningBlock({ text, streaming }: { text: string; streaming: boolean }) {
  const [open, setOpen] = useState(streaming);
  useEffect(() => {
    if (!streaming) setOpen(false);
  }, [streaming]);
  return (
    <div className="lgui-reasoning">
      <button type="button" className="lgui-reasoning-head" onClick={() => setOpen((v) => !v)} aria-expanded={open}>
        <ChevronDown size={14} className={`lgui-reasoning-chev${open ? ' lgui-reasoning-chev-open' : ''}`} />
        {streaming ? '思考中…' : '查看思考过程'}
      </button>
      {open ? <pre className="lgui-reasoning-body">{text}</pre> : null}
    </div>
  );
}

function AssistantBody({ text, reasoning, streaming }: { text: string; reasoning?: string; streaming: boolean }) {
  const segments = parseSegments(text);
  const hasReasoning = !!reasoning && reasoning.trim().length > 0;
  if (segments.length === 0) {
    if (!streaming) return <p className="lgui-prose">（空回复）</p>;
    return hasReasoning ? (
      <ReasoningBlock text={reasoning} streaming={streaming} />
    ) : (
      <div className="lgui-pending" role="status">模型思考中…</div>
    );
  }
  return (
    <>
      {hasReasoning ? <ReasoningBlock text={reasoning} streaming={streaming} /> : null}
      {segments.map((seg, i) => {
        if (seg.kind === 'text') return <div key={i} className="lgui-markdown"><MarkdownBody text={seg.text} streaming={streaming} /></div>;
        if (seg.kind === 'widget') return <WidgetRenderer key={i} data={seg.data} />;
        if (seg.kind === 'pending') return <div key={i} className="lgui-pending" role="status">正在生成卡片…</div>;
        return (
          <div key={i} className="lgui-widget">
            <div className="lgui-widget-title"><h4>JSON 解析失败（原样）</h4></div>
            <pre className="lgui-broken">{seg.raw}</pre>
          </div>
        );
      })}
    </>
  );
}

function ActionRow({ onRegenerate, onCopy, disabled }: { onRegenerate: () => void; onCopy: () => Promise<boolean>; disabled: boolean }) {
  const [copied, setCopied] = useState(false);
  const [vote, setVote] = useState<'up' | 'down' | null>(null);
  const [saved, setSaved] = useState(false);
  const copyTimer = useRef<number | null>(null);
  useEffect(() => () => {
    if (copyTimer.current !== null) window.clearTimeout(copyTimer.current);
  }, []);
  const copy = async () => {
    if (!(await onCopy())) return;
    setCopied(true);
    if (copyTimer.current !== null) window.clearTimeout(copyTimer.current);
    copyTimer.current = window.setTimeout(() => setCopied(false), 1600);
  };
  return (
    <div className="lgui-actions">
      <button type="button" title="重新生成" aria-label="重新生成" disabled={disabled} onClick={onRegenerate}><RefreshCw size={16} /></button>
      <button type="button" title={copied ? '已复制' : '复制回答'} aria-label={copied ? '已复制' : '复制'} onClick={() => void copy()}>{copied ? <Check size={16} /> : <Copy size={16} />}</button>
      <button type="button" title="点赞" aria-label="点赞" aria-pressed={vote === 'up'} className={vote === 'up' ? 'lgui-action-active' : undefined} onClick={() => setVote((value) => value === 'up' ? null : 'up')}><ThumbsUp size={16} /></button>
      <button type="button" title="点踩" aria-label="点踩" aria-pressed={vote === 'down'} className={vote === 'down' ? 'lgui-action-active' : undefined} onClick={() => setVote((value) => value === 'down' ? null : 'down')}><ThumbsDown size={16} /></button>
      <button type="button" title="分享（演示占位）" aria-label="分享" disabled><Share2 size={16} /></button>
      <button type="button" title={saved ? '取消收藏' : '收藏回答'} aria-label={saved ? '取消收藏' : '收藏'} aria-pressed={saved} className={saved ? 'lgui-action-active' : undefined} onClick={() => setSaved((value) => !value)}><Bookmark size={16} fill={saved ? 'currentColor' : 'none'} /></button>
      <button type="button" title="更多操作（演示占位）" aria-label="更多" disabled><MoreHorizontal size={16} /></button>
    </div>
  );
}

/* ── 页面 ──────────────────────────────────────────────────── */

const THREADS = [
  { title: 'LanguageGUI 演示会话', meta: '进行中 · 真实模型', active: true },
  { title: '跨时区会议时间协调', meta: '示例' },
  { title: '季度用量与成本分析', meta: '示例' },
  { title: '帮我起草发布邮件', meta: '示例' },
];

const WELCOME_SUGGESTIONS = [
  '现在旧金山、东京、柏林各几点？我们约周会',
  '会费预算 $2,480.58，换成欧元是多少？',
  '对比 gpt-4o / claude / deepseek 本周请求量，画个柱状图',
];

const DOMAIN_TEMPLATE_DOCUMENT = mergeContentDocuments(
  currencyTemplate({
    from: { code: 'USD', name: 'US Dollar', amount: '$2,480.58', rate: '1 USD = 0.90 EUR' },
    to: { code: 'EUR', name: 'Euro', amount: '€2,232.52', rate: '1 EUR = 1.11 USD' },
    source: { label: '演示汇率' },
  }),
  weatherTemplate({
    location: '上海', temperature: '28°', condition: '晴', high: '31°', low: '24°', aqi: '42', uv: '3 / 10',
    hourly: [
      { time: '现在', temperature: '28°', condition: '晴' },
      { time: '14:00', temperature: '30°', condition: '晴' },
      { time: '16:00', temperature: '29°', condition: '多云' },
    ],
    source: { label: '演示天气' },
  }),
  stockTemplate({
    name: 'Meta Platforms Inc', symbol: 'META', price: '$547.10', delta: '+1.12%', open: '$546.20', previousClose: '$548.22',
    labels: ['10:00', '12:00', '14:00', '16:00'], values: [546.2, 548.4, 544.1, 547.1], unit: '$',
    source: { label: '演示行情' },
  }),
  scoreTemplate({ league: 'Spain League', status: 'Live · 63:24', home: { name: 'Team #1', score: 3 }, away: { name: 'Team #2', score: 1 }, date: '2026-08-27' }),
  ratingTemplate('这组结构化展示对你有帮助吗？'),
);

const DEMO_MESSAGES: ChatMessage[] = [
  { role: 'user', text: '帮我做一份本周团队效率简报，要有结论、数据和下一步建议。' },
  {
    role: 'assistant',
    model: DEFAULT_MODEL,
    text: [
      '# 本周团队效率简报',
      '',
      '下面是一份基于演示数据的快速复盘。整体来看，**交付速度变快了**，但评审等待仍是最值得优先处理的瓶颈。',
      '',
      '> [!TIP] 这是 LanguageGUI 的示例输出，卡片中的数值为演示数据，不代表真实业务指标。',
      '',
      '## 先看结论',
      '',
      '- 平均交付周期从 `3.8 天` 降至 **3.1 天**，改善约 18%。',
      '- 自动化检查覆盖率达到 **86%**，回归缺陷下降 12%。',
      '- 评审等待占总周期约 31%，建议为高优先级变更设置轮值 reviewer。',
      '',
      '## 指标快照',
      '',
      '| 指标 | 上周 | 本周 | 变化 |',
      '| --- | ---: | ---: | ---: |',
      '| 交付周期 | 3.8 天 | 3.1 天 | +18% |',
      '| 合并请求 | 42 | 57 | +36% |',
      '| 回归缺陷 | 8 | 7 | -12% |',
      '| 自动化覆盖 | 79% | 86% | +7pp |',
      '',
      '## 开发实现',
      '',
      '代码块支持文件名、语言标识、行号、重点行、复制与导出，和普通正文保持同一阅读轨。',
      '',
      '```ts filename=src/metrics/delivery-summary.ts {4,7-9}',
      'export interface DeliverySummary {',
      '  cycleTime: number;',
      '  automationCoverage: number;',
      '  reviewWaitShare: number;',
      '}',
      '',
      'export function deliveryVerdict(input: DeliverySummary) {',
      '  const onTarget = input.cycleTime <= 3;',
      '  return onTarget ? "达标" : "继续优化";',
      '}',
      '```',
      '',
      '## 项目评审摘要',
      '',
      '评审、审计和验证类结果会收敛为专门的摘要块，先给结论，再按严重度展开证据和下一步。',
      '',
      '```languagegui',
      JSON.stringify({
        version: 'languagegui/v1',
        blocks: [
          {
            type: 'review-summary',
            title: '代码评审摘要',
            description: '需求拆解、主要问题与验证结果',
            verdict: 'changes_requested',
            summary: '主链路已经完整，但仍有 1 项高风险问题和 1 个失败门禁需要在合并前处理。',
            stats: { files: 6, findings: 3, passed: 3 },
            findings: [
              {
                severity: 'high',
                title: '等待审批的运行会被统一超时中断',
                detail: '当前超时判断把 waiting_approval 计入所有 active run，超过 60 秒后可能误伤人工审批。',
                file: 'web/src/pages/chat.page.tsx',
                line: 219,
                evidence: 'ACTIVE_STATES.has(run.status) && age > RUN_TIMEOUT_MS',
                suggestion: '排除 waiting_approval，并从 run 创建时间计算真正的执行超时。',
              },
              {
                severity: 'medium',
                title: '模型 effort 未形成端到端断言',
                detail: '配置层已经声明 reasoning effort，但 adapter 请求缺少防回归测试。',
                file: 'internal/runtime/adapters/codexapp/codexapp.go',
                line: 568,
                suggestion: '补一条 turn/start.effort 的请求快照测试。',
              },
              {
                severity: 'low',
                title: '时间线条件数组仍触发 Hooks 警告',
                file: 'web/src/pages/chat.page.tsx',
                line: 214,
                suggestion: '把派生数组收进 useMemo，保持依赖稳定。',
              },
            ],
            checks: [
              { label: 'Go build', status: 'passed', detail: '全部包通过', command: 'go build ./...' },
              { label: 'Go race tests', status: 'passed', detail: '4 个触面包通过', command: 'go test -race ./internal/...' },
              { label: 'TypeScript', status: 'passed', detail: '类型检查通过', command: 'pnpm tsc -b' },
              { label: 'ESLint', status: 'failed', detail: '2 errors · 4 warnings', command: 'pnpm lint' },
            ],
            next_steps: [
              { label: '修复超时判定', detail: '先处理可能中断真实运行的高风险问题。' },
              { label: '清理 lint 门禁', detail: '补齐两个回归断言后重新执行前后端触面检查。' },
            ],
            source: { label: '演示评审数据' },
          },
        ],
      }),
      '```',
      '',
      '趋势可以用下面这张图快速扫读：',
      '',
      '```lgui',
      '{"widget":"chart","title":"本周完成量（件）","labels":["周一","周二","周三","周四","周五"],"values":[8,11,9,15,14],"unit":""}',
      '```',
      '',
      '## 分时区协作',
      '',
      '为了减少跨时区等待，下面列出当前几个协作节点。建议把需要同步讨论的议题安排在柏林下午、东京晚间的重叠窗口。',
      '',
      '```lgui',
      '{"widget":"clock","cities":[{"city":"旧金山","tz":"PST","time":"8:40 AM","date":"周三 · 2 月 21 日","active":false},{"city":"柏林","tz":"CET","time":"5:40 PM","date":"周三 · 2 月 21 日","active":true},{"city":"东京","tz":"JST","time":"1:40 AM","date":"周四 · 2 月 22 日","active":false}]}',
      '```',
      '',
      '## 下一步',
      '',
      '1. 给评审队列增加一位轮值 reviewer，并在 `24 小时` 后自动提醒。',
      '2. 保持当前自动化检查门槛，针对失败最多的接口测试补齐用例。',
      '3. 下周继续观察交付周期，目标是稳定在 3 天以内。',
      '',
      '你可以继续追问成本、时间或满意度，我会把适合结构化展示的部分转换成卡片。',
      '',
      '```lgui',
      '{"widget":"table","title":"下周行动清单","columns":["行动","负责人","优先级"],"rows":[["评审轮值","林晓","高"],["接口回归用例","周宁","中"],["周期复盘","Mauro","低"]]}',
      '```',
      '',
      '## 结构化交付物',
      '',
      '下面这组卡片使用生产 Chat 共用的 `languagegui/v1` ContentBlock 协议，展示指标、趋势、数据表、文件和日程。',
      '',
      '```languagegui',
      JSON.stringify({
        version: 'languagegui/v1',
        blocks: [
          {
            type: 'metric',
            title: '交付健康度',
            description: '本周核心指标（演示数据）',
            items: [
              { label: '平均交付周期', value: '3.1 天', delta: '改善 18%', tone: 'positive', detail: '目标 3 天以内' },
              { label: '自动化覆盖', value: '86%', delta: '+7pp', tone: 'positive' },
              { label: '评审等待占比', value: '31%', delta: '需要关注', tone: 'warning' },
            ],
          },
          {
            type: 'chart',
            chart: 'bar',
            title: '本周完成量',
            labels: ['周一', '周二', '周三', '周四', '周五'],
            series: [{ name: '完成量', values: [8, 11, 9, 15, 14] }],
            unit: ' 件',
            source: { label: '演示数据' },
          },
          {
            type: 'table',
            title: '行动负责人',
            columns: [
              { key: 'action', label: '行动' },
              { key: 'owner', label: '负责人' },
              { key: 'priority', label: '优先级', align: 'center' },
            ],
            rows: [
              { action: '评审轮值', owner: '林晓', priority: '高' },
              { action: '接口回归用例', owner: '周宁', priority: '中' },
              { action: '周期复盘', owner: 'Mauro', priority: '低' },
            ],
          },
          {
            type: 'file',
            title: '已生成文件',
            files: [{ name: 'weekly-efficiency-report.csv', mime: 'text/csv', size: '24 KB', status: 'ready' }],
          },
          {
            type: 'event',
            title: '下周周期复盘',
            start: '2026-08-31T16:00:00+08:00',
            end: '2026-08-31T16:45:00+08:00',
            location: '线上会议室',
            timezone: 'Asia/Shanghai',
          },
          {
            type: 'image',
            title: '交付预览',
            images: [{ src: shanShuiPanorama, alt: '水墨山水全景', caption: '复用工作台已有视觉资产' }],
          },
          {
            type: 'map',
            title: '复盘地点',
            location: '上海市',
            latitude: 31.2304,
            longitude: 121.4737,
            url: 'https://www.openstreetmap.org/?mlat=31.2304&mlon=121.4737#map=12/31.2304/121.4737',
          },
          {
            type: 'search',
            title: '参考来源',
            query: 'LanguageGUI UI Kit',
            results: [
              { title: 'LanguageGUI — A UI Kit for LLMs', url: 'https://languagegui.com/', source: 'languagegui.com', snippet: '官方 UI Kit、widgets、prompt boxes 与 multi-prompt workflow 说明。' },
            ],
          },
        ],
      }),
      '```',
      '',
      '## 领域模板',
      '',
      '汇率、天气、股票和比分由通用 ContentBlock 组合，不引入重复的领域 wire schema。',
      '',
      '```languagegui',
      JSON.stringify(DOMAIN_TEMPLATE_DOCUMENT),
      '```',
    ].join('\n'),
  },
];

export default function LanguageGuiDemoPage() {
  const [messages, setMessages] = useState<ChatMessage[]>(() => {
    if (typeof window !== 'undefined' && new URLSearchParams(window.location.search).has('ask')) return [];
    return DEMO_MESSAGES;
  });
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [models, setModels] = useState<ModelOption[]>([]);
  const [model, setModel] = useState(DEFAULT_MODEL);
  const [menuOpen, setMenuOpen] = useState(false);
  const streamRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const initialScrollSkipped = useRef(false);
  const modelSelectRef = useRef<HTMLDivElement | null>(null);
  // StrictMode 的 updater 双调用不允许带副作用：历史与流式锁走 ref，send 在 updater 外发起。
  const messagesRef = useRef<ChatMessage[]>([]);
  const busyRef = useRef(false);

  // 模型菜单：外点 / Esc 关闭（下拉的基本可用性）。
  useEffect(() => {
    if (!menuOpen) return;
    const onDown = (e: MouseEvent) => {
      if (modelSelectRef.current && !modelSelectRef.current.contains(e.target as Node)) setMenuOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setMenuOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [menuOpen]);
  useEffect(() => {
    messagesRef.current = messages;
  }, [messages]);

  useEffect(() => {
    void fetch(`${API}/models`)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error(String(r.status)))))
      .then((d: { models: ModelOption[] }) => setModels(d.models))
      .catch(() => setModels([{ ref: DEFAULT_MODEL, display: 'DeepSeek V4 Flash', provider: 'DeepSeek' }]));
  }, []);

  useEffect(() => {
    if (!initialScrollSkipped.current) {
      initialScrollSkipped.current = true;
      const stream = scrollRef.current;
      stream?.scrollTo({ top: 0, behavior: 'auto' });
      if (stream) {
        window.requestAnimationFrame(() => stream.scrollTo({ top: 0, behavior: 'auto' }));
      }
      return;
    }
    scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: 'smooth' });
  }, [messages]);

  const send = useCallback(async (raw: string, history: ChatMessage[]) => {
    const text = raw.trim();
    if (!text || busyRef.current) return;
    busyRef.current = true;
    setError(null);
    const next: ChatMessage[] = [...history, { role: 'user', text }, { role: 'assistant', text: '', model }];
    messagesRef.current = next;
    setMessages(next);
    setStreaming(true);
    const controller = new AbortController();
    streamRef.current = controller;
    let buffer = '';
    let reasonBuffer = '';
    try {
      const resp = await fetch(`${API}/chat`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        signal: controller.signal,
        body: JSON.stringify({
          model,
          messages: [
            { role: 'system', content: SYSTEM_PROMPT },
            // ChatMessage 用 {role,text} 存本地，发给 API 必须映射成 {role,content}。
            ...next.slice(0, -1).map((m) => ({ role: m.role, content: m.text })),
          ].slice(-13),
        }),
      });
      if (!resp.ok || !resp.body) {
        const detail = await resp.json().catch(() => ({ error: `HTTP ${resp.status}` }));
        throw new Error(String(detail.error || `HTTP ${resp.status}`));
      }
      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let sseRest = '';
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        sseRest += decoder.decode(value, { stream: true });
        const lines = sseRest.split('\n');
        sseRest = lines.pop() ?? '';
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue;
          const payload = line.slice(6).trim();
          if (payload === '[DONE]') continue;
          try {
            const parsed = JSON.parse(payload);
            const delta: string = parsed.choices?.[0]?.delta?.content ?? '';
            // 思考模型（deepseek-v4-flash 等）先吐 reasoning_content，单独累积折叠展示。
            const reasonDelta: string = parsed.choices?.[0]?.delta?.reasoning_content ?? '';
            if (delta || reasonDelta) {
              buffer += delta;
              reasonBuffer += reasonDelta;
              setMessages((cur) => {
                const copy = [...cur];
                const last = copy[copy.length - 1];
                copy[copy.length - 1] = { ...last, text: buffer, reasoning: reasonBuffer || undefined };
                return copy;
              });
            }
          } catch {
            /* 上游偶发半行 JSON：跳过，下一行补齐 */
          }
        }
      }
      if (!buffer) throw new Error('模型没有返回内容');
    } catch (err) {
      if (controller.signal.aborted) return;
      const message = err instanceof TypeError ? '无法连接本地模型代理：先运行 node scripts/languagegui-proxy.mjs' : String((err as Error).message || err);
      setError(message);
      setMessages((cur) => cur.slice(0, -1));
    } finally {
      setStreaming(false);
      streamRef.current = null;
      busyRef.current = false;
    }
  }, [model]);

  const ask = useCallback((text: string) => {
    void send(text, messagesRef.current);
  }, [send]);

  const regenerate = useCallback(() => {
    const history = [...messagesRef.current];
    if (history.at(-1)?.role === 'assistant') history.pop();
    const lastUser = history.at(-1)?.role === 'user' ? history.pop() : undefined;
    if (lastUser) void send(lastUser.text, history);
  }, [send]);

  // ?ask= 直达提问（演示/截图用）
  useEffect(() => {
    const q = new URLSearchParams(window.location.search).get('ask');
    if (q) ask(q);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!input.trim() || streaming) return;
    ask(input);
    setInput('');
  };

  const currentModel = models.find((m) => m.ref === model);

  return (
    <div className="lgui">
      <aside className="lgui-sidebar">
        <div className="lgui-brand">
          <span className="lgui-avatar" />
          <span className="lgui-brand-name">LanguageGUI</span>
        </div>
        <button type="button" className="lgui-new-chat" onClick={() => { streamRef.current?.abort(); setMessages([]); setError(null); }}>
          <Plus size={16} /> 新建对话
        </button>
        <div className="lgui-threads" role="list">
          <div className="lgui-nav-label">会话</div>
          {THREADS.map((t) => (
            <button key={t.title} type="button" role="listitem" className={`lgui-thread${t.active ? ' lgui-thread-active' : ''}`}>
              <span className="lgui-thread-title">{t.title}</span>
              <span className="lgui-thread-meta">{t.meta}</span>
            </button>
          ))}
        </div>
        <div className="lgui-sidebar-foot">
          <div className="lgui-user">
            <span className="lgui-user-photo" />
            <span>
              <div className="lgui-user-name">Mauro Sicard</div>
              <div className="lgui-user-plan">Tonki Labs · Pro</div>
            </span>
          </div>
          <a className="lgui-back" href="/chat">← 返回工作台</a>
        </div>
      </aside>

      <main className="lgui-main">
        <header className="lgui-topbar">
          <div>
            <span className="lgui-topbar-title">LanguageGUI 演示会话</span>
            <span className="lgui-model-chip">
              <Gauge size={12} /> {currentModel?.display ?? model} · 富输出模式
            </span>
          </div>
          <div className="lgui-topbar-actions">
            <div className="lgui-model-select" ref={modelSelectRef}>
              <button type="button" className="lgui-icon-btn lgui-icon-btn-wide" aria-label="切换模型" aria-expanded={menuOpen} aria-haspopup="menu" onClick={() => setMenuOpen((v) => !v)}>
                {currentModel?.display ?? model} <ChevronDown size={13} />
              </button>
              {menuOpen ? (
                <div className="lgui-menu" role="menu">
                  {models.map((m) => (
                    <button
                      key={m.ref}
                      type="button"
                      role="menuitem"
                      className={`lgui-menu-item${m.ref === model ? ' lgui-menu-item-active' : ''}`}
                      onClick={() => { setModel(m.ref); setMenuOpen(false); }}
                    >
                      <span>{m.display}</span>
                      <span className="lgui-menu-provider">{m.provider}</span>
                      {m.ref === model ? <Check size={14} /> : null}
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
            <button type="button" className="lgui-icon-btn" aria-label="编辑标题（演示占位）" title="编辑标题（演示占位）" disabled><Pencil size={15} /></button>
            <button type="button" className="lgui-icon-btn" aria-label="设置（演示占位）" title="设置（演示占位）" disabled><Settings size={15} /></button>
          </div>
        </header>

        <div className="lgui-stream" ref={scrollRef}>
          {messages.length === 0 ? (
            <section className="lgui-turn">
              <p className="lgui-prose">
                这是 LanguageGUI 部件协议的实机演示：我的回答会以文本 + 结构化 widget 卡（世界时钟 / 汇率 / 数据表 / 柱状图 / 评分）的形式渲染。
                点一条建议试试——
              </p>
            </section>
          ) : null}

          {messages.map((m, i) =>
            m.role === 'user' ? (
              <div key={i} className="lgui-turn lgui-turn-user">
                <div className="lgui-bubble">
                  <p className="lgui-prose">{m.text}</p>
                </div>
              </div>
            ) : (
              <section key={i} className="lgui-turn">
                <AssistantBody text={m.text} reasoning={m.reasoning} streaming={streaming && i === messages.length - 1} />
                {!(streaming && i === messages.length - 1) ? (
                  <ActionRow disabled={streaming} onRegenerate={() => regenerate()} onCopy={() => copyText(m.text)} />
                ) : null}
              </section>
            ),
          )}

          <LanguageGuiToolShowcase />

          {error ? <div className="lgui-error" role="alert">{error}</div> : null}
        </div>

        <form className="lgui-composer-wrap" onSubmit={onSubmit}>
          {messages.length === 0 ? (
            <div className="lgui-suggestions">
              {WELCOME_SUGGESTIONS.map((s) => (
                <button key={s} type="button" className="lgui-suggestion" onClick={() => ask(s)}>{s}</button>
              ))}
            </div>
          ) : null}
          <div className="lgui-composer">
            <input
              className="lgui-composer-input"
              placeholder={streaming ? '正在生成，可点右侧停止…' : 'How can I help you?'}
              aria-label="输入消息"
              value={input}
              disabled={streaming}
              onChange={(e) => setInput(e.target.value)}
            />
            <div className="lgui-composer-bar">
              <div className="lgui-composer-tools">
                <button type="button" className="lgui-icon-btn" aria-label="附件（演示占位）" title="附件（演示占位）" disabled><Paperclip size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="语音输入（演示占位）" title="语音输入（演示占位）" disabled><Mic size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="图表（演示占位）" title="图表（演示占位）" disabled><BarChart3 size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="时钟（演示占位）" title="时钟（演示占位）" disabled><Clock3 size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="会话（演示占位）" title="会话（演示占位）" disabled><MessageSquare size={15} /></button>
              </div>
              {streaming ? (
                <button type="button" className="lgui-send" aria-label="停止生成" onClick={() => streamRef.current?.abort()}>
                  <span className="lgui-stop" />
                </button>
              ) : (
                <button type="submit" className="lgui-send" aria-label="发送">
                  <Navigation size={16} />
                </button>
              )}
            </div>
          </div>
        </form>
      </main>
    </div>
  );
}
