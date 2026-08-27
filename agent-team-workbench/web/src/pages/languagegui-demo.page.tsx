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
  const str = (k: string) => (typeof data[k] === 'string' ? (data[k] as string) : '');
  return (
    <div className="lgui-widget">
      <span className="lgui-amount-label">Amount · {str('from')}</span>
      <div className="lgui-amount">{str('amount')}</div>
      <div className="lgui-fx-row">
        <span className="lgui-fx-flag">{currencyFlag(str('from'))}</span>
        <span className="lgui-fx-code">
          <b>{str('from')}</b>
          <span>{str('rate')}</span>
        </span>
        <span className="lgui-fx-value">{str('amount')}</span>
      </div>
      <div className="lgui-fx-swap">
        <button type="button" aria-label="交换币种">
          <ArrowLeftRight size={15} />
        </button>
      </div>
      <div className="lgui-fx-row">
        <span className="lgui-fx-flag">{currencyFlag(str('to'))}</span>
        <span className="lgui-fx-code">
          <b>{str('to')}</b>
          <span>{str('rate')}</span>
        </span>
        <span className="lgui-fx-value">{str('result')}</span>
      </div>
    </div>
  );
}

type TableCell = string | number | { text?: string; tone?: string };

function TableWidget({ data }: { data: LguiWidgetData }) {
  const columns = Array.isArray(data.columns) ? (data.columns as unknown[]) : [];
  const rows = Array.isArray(data.rows) ? (data.rows as unknown[]) : [];
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
                <th key={i} className={i > 0 ? 'lgui-num' : undefined}>{String(c)}</th>
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
                const text = obj ? String(obj.text ?? '') : String(cell);
                const cls = ['lgui-table-cell', c > 0 ? 'lgui-num' : '', tone === 'up' ? 'lgui-delta-up' : tone === 'down' ? 'lgui-delta-down' : ''].filter(Boolean).join(' ');
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
  return (
    <div className="lgui-widget lgui-rating">
      <div className="lgui-rating-text">
        <b>{question}</b>
        <span>演示件：反馈不上传</span>
      </div>
      <div className="lgui-stars" aria-label="评分">
        {[1, 2, 3, 4, 5].map((i) => (
          <Star key={i} size={22} />
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
        if (seg.kind === 'text') return <p key={i} className="lgui-prose">{seg.text}</p>;
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

function ActionRow({ onRegenerate, onCopy, disabled }: { onRegenerate: () => void; onCopy: () => void; disabled: boolean }) {
  return (
    <div className="lgui-actions">
      <button type="button" aria-label="重新生成" disabled={disabled} onClick={onRegenerate}><RefreshCw size={16} /></button>
      <button type="button" aria-label="复制" onClick={onCopy}><Copy size={16} /></button>
      <button type="button" aria-label="点赞"><ThumbsUp size={16} /></button>
      <button type="button" aria-label="点踩"><ThumbsDown size={16} /></button>
      <button type="button" aria-label="分享"><Share2 size={16} /></button>
      <button type="button" aria-label="收藏"><Bookmark size={16} /></button>
      <button type="button" aria-label="更多"><MoreHorizontal size={16} /></button>
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

export default function LanguageGuiDemoPage() {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [models, setModels] = useState<ModelOption[]>([]);
  const [model, setModel] = useState(DEFAULT_MODEL);
  const [menuOpen, setMenuOpen] = useState(false);
  const streamRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement | null>(null);
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
              <button type="button" className="lgui-icon-btn lgui-icon-btn-wide" aria-label="切换模型" onClick={() => setMenuOpen((v) => !v)}>
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
            <button type="button" className="lgui-icon-btn" aria-label="编辑标题"><Pencil size={15} /></button>
            <button type="button" className="lgui-icon-btn" aria-label="设置"><Settings size={15} /></button>
          </div>
        </header>

        <div className="lgui-stream" ref={scrollRef}>
          {messages.length === 0 ? (
            <section className="lgui-turn">
              <div className="lgui-msg-head">
                <span className="lgui-avatar lgui-avatar-sm" />
                <span className="lgui-msg-name">LanguageGUI</span>
                <span className="lgui-msg-divider">|</span>
                <span className="lgui-msg-time">真实模型 · {currentModel?.display ?? model}</span>
              </div>
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
                  <div className="lgui-bubble-head">
                    <span className="lgui-user-photo" style={{ width: 22, height: 22 }} />
                    <span className="lgui-bubble-name">你</span>
                  </div>
                  <p className="lgui-prose">{m.text}</p>
                </div>
              </div>
            ) : (
              <section key={i} className="lgui-turn">
                <div className="lgui-msg-head">
                  <span className="lgui-avatar lgui-avatar-sm" />
                  <span className="lgui-msg-name">LanguageGUI</span>
                  <span className="lgui-msg-divider">|</span>
                  <span className="lgui-msg-time">{models.find((x) => x.ref === m.model)?.display ?? m.model ?? model}</span>
                </div>
                <AssistantBody text={m.text} reasoning={m.reasoning} streaming={streaming && i === messages.length - 1} />
                {!(streaming && i === messages.length - 1) ? (
                  <ActionRow disabled={streaming} onRegenerate={() => regenerate()} onCopy={() => void navigator.clipboard?.writeText(m.text).catch(() => {})} />
                ) : null}
              </section>
            ),
          )}

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
                <button type="button" className="lgui-icon-btn" aria-label="附件"><Paperclip size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="语音输入"><Mic size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="图表"><BarChart3 size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="时钟"><Clock3 size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="会话"><MessageSquare size={15} /></button>
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
