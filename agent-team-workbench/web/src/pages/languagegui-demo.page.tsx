/**
 * LanguageGUI 演示页（支线 zcode/languagegui-demo）。
 * 复刻 Tonki Labs「LanguageGUI — A UI Kit for LLMs」（MIT）的设计语言与
 * conversational widget 思路：LLM 回答不再直排 markdown，而是渲染成
 * 时钟卡/汇率卡/数据表/评分/语音回复等结构化界面。
 * 独立路由 /languagegui，mock 数据、不接后端、不进工作台语义 token 体系
 *（色值全部收在 languagegui-demo.css 的 .lgui 作用域，门禁手法同 tx-scope）。
 */

import {
  ArrowLeftRight,
  BarChart3,
  Bookmark,
  Clock3,
  Copy,
  Gauge,
  MessageSquare,
  Mic,
  MoreHorizontal,
  Navigation,
  Paperclip,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Settings,
  Share2,
  Star,
  ThumbsDown,
  ThumbsUp,
} from 'lucide-react';
import './languagegui-demo.css';

/** 拟物钟面：SVG 指针角度即时间，颜色走 currentColor 由 CSS 控制。 */
function ClockFace({ hour, minute, dim = false }: { hour: number; minute: number; dim?: boolean }) {
  const hourAngle = ((hour % 12) + minute / 60) * 30;
  const minuteAngle = minute * 6;
  return (
    <svg
      className={`lgui-clock-face${dim ? ' lgui-clock-dim' : ''}`}
      width="64"
      height="64"
      viewBox="0 0 64 64"
      aria-hidden
    >
      <circle cx="32" cy="32" r="29" fill="var(--lg-surface)" stroke="var(--lg-border)" strokeWidth="2" />
      <circle cx="32" cy="32" r="29" fill="none" stroke="var(--lg-border-active)" strokeWidth="1" opacity="0.5" />
      {Array.from({ length: 12 }, (_, i) => (
        <line
          key={i}
          x1="32"
          y1="8"
          x2="32"
          y2="11"
          stroke="var(--lg-border-active)"
          strokeWidth="1.5"
          strokeLinecap="round"
          transform={`rotate(${i * 30} 32 32)`}
        />
      ))}
      <line
        x1="32"
        y1="32"
        x2="32"
        y2="19"
        stroke="var(--lg-ink)"
        strokeWidth="3"
        strokeLinecap="round"
        transform={`rotate(${hourAngle} 32 32)`}
      />
      <line
        x1="32"
        y1="32"
        x2="32"
        y2="13"
        stroke="var(--lg-accent)"
        strokeWidth="2"
        strokeLinecap="round"
        transform={`rotate(${minuteAngle} 32 32)`}
      />
      <circle cx="32" cy="32" r="2.5" fill="var(--lg-ink)" />
    </svg>
  );
}

function AssistantHead({ time }: { time: string }) {
  return (
    <div className="lgui-msg-head">
      <span className="lgui-avatar lgui-avatar-sm" />
      <span className="lgui-msg-name">LanguageGUI</span>
      <span className="lgui-msg-divider">|</span>
      <span className="lgui-msg-time">{time}</span>
    </div>
  );
}

function UserBubble({ name, time, text }: { name: string; time: string; text: string }) {
  return (
    <div className="lgui-turn lgui-turn-user">
      <div className="lgui-bubble">
        <div className="lgui-bubble-head">
          <span className="lgui-user-photo" style={{ width: 22, height: 22 }} />
          <span className="lgui-bubble-name">{name}</span>
          <span className="lgui-msg-time">{time}</span>
        </div>
        <p className="lgui-prose">{text}</p>
      </div>
    </div>
  );
}

function ClockWidget() {
  const cities = [
    { city: 'San Francisco', tz: 'PST', time: '2:40 PM', date: 'Friday, Jan 19', hour: 14, minute: 40, active: true },
    { city: '东京', tz: 'JST', time: '7:40 AM', date: 'Saturday, Jan 20', hour: 7, minute: 40, active: false },
    { city: 'Berlin', tz: 'CET', time: '11:44 PM', date: 'Friday, Jan 19', hour: 23, minute: 44, active: false },
  ];
  return (
    <div className="lgui-widget-stack">
      {cities.map((c) => (
        <div key={c.city} className={`lgui-widget lgui-clock-row${c.active ? ' lgui-widget-active' : ''}`}>
          <div className="lgui-clock-city">
            <b>{c.city}</b>
            <span className="lgui-chip">{c.tz}</span>
          </div>
          <div className={`lgui-clock-time${c.active ? '' : ' lgui-clock-dim'}`}>
            <b>{c.time}</b>
            <span>{c.date}</span>
          </div>
          <ClockFace hour={c.hour} minute={c.minute} dim={!c.active} />
        </div>
      ))}
    </div>
  );
}

function FxWidget() {
  return (
    <div className="lgui-widget">
      <span className="lgui-amount-label">Amount · 美元</span>
      <div className="lgui-amount">$2,480.58</div>
      <div className="lgui-fx-row">
        <span className="lgui-fx-flag">🇺</span>
        <span className="lgui-fx-code">
          <b>USD</b>
          <span>US dollar · 1 USD = 0.90 EUR</span>
        </span>
        <span className="lgui-fx-value">$2,480.58</span>
      </div>
      <div className="lgui-fx-swap">
        <button type="button" aria-label="交换币种">
          <ArrowLeftRight size={15} />
        </button>
      </div>
      <div className="lgui-fx-row">
        <span className="lgui-fx-flag">🇪</span>
        <span className="lgui-fx-code">
          <b>EUR</b>
          <span>Euro · 1 EUR = 1.1 USD</span>
        </span>
        <span className="lgui-fx-value">€2,232.52</span>
      </div>
    </div>
  );
}

function TableWidget() {
  const bars = [38, 52, 44, 66, 58, 80, 72, 96, 84, 62, 74, 90];
  return (
    <div className="lgui-widget">
      <div className="lgui-widget-title">
        <h4>本周 token 用量</h4>
        <span className="lgui-chip">Jan 13 – Jan 19</span>
      </div>
      <div className="lgui-spark" aria-hidden>
        {bars.map((h, i) => (
          <i key={i} className={h > 85 ? 'lgui-spark-hi' : undefined} style={{ height: `${h * 0.44}px` }} />
        ))}
      </div>
      <table className="lgui-table">
        <thead>
          <tr>
            <th>模型</th>
            <th className="lgui-num">请求数</th>
            <th className="lgui-num">Tokens</th>
            <th className="lgui-num">环比</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>gpt-4o</td>
            <td className="lgui-num">1,284</td>
            <td className="lgui-num">2.1M</td>
            <td className="lgui-num lgui-delta-up">+12.4%</td>
          </tr>
          <tr>
            <td>claude-sonnet</td>
            <td className="lgui-num">946</td>
            <td className="lgui-num">1.6M</td>
            <td className="lgui-num lgui-delta-up">+8.1%</td>
          </tr>
          <tr>
            <td>deepseek-chat</td>
            <td className="lgui-num">2,051</td>
            <td className="lgui-num">3.4M</td>
            <td className="lgui-num lgui-delta-down">−4.7%</td>
          </tr>
        </tbody>
      </table>
    </div>
  );
}

function RatingWidget() {
  return (
    <div className="lgui-widget lgui-rating">
      <div className="lgui-rating-text">
        <b>这次回答对你有帮助吗？</b>
        <span>你的反馈会用于优化后续回复风格</span>
      </div>
      <div className="lgui-stars" aria-label="评分 4 / 5">
        {[1, 2, 3, 4].map((i) => (
          <Star key={i} size={22} fill="currentColor" />
        ))}
        <Star size={22} />
      </div>
    </div>
  );
}

function VoiceWidget() {
  const wave = [8, 14, 22, 12, 26, 18, 30, 22, 12, 20, 26, 10, 16, 24, 14, 28, 18, 10, 22, 16, 26, 12, 18, 8, 14, 20, 10, 16];
  return (
    <div className="lgui-widget lgui-voice">
      <button type="button" className="lgui-play" aria-label="播放语音回复">
        <Play size={16} fill="currentColor" />
      </button>
      <div className="lgui-wave" aria-hidden>
        {wave.map((h, i) => (
          <i key={i} className={i < 11 ? 'lgui-wave-played' : undefined} style={{ height: `${h}px` }} />
        ))}
      </div>
      <span className="lgui-voice-time">0:07 / 0:19</span>
    </div>
  );
}

function ActionRow() {
  return (
    <div className="lgui-actions">
      <button type="button" aria-label="重新生成"><RefreshCw size={16} /></button>
      <button type="button" aria-label="复制"><Copy size={16} /></button>
      <button type="button" aria-label="点赞"><ThumbsUp size={16} /></button>
      <button type="button" aria-label="点踩"><ThumbsDown size={16} /></button>
      <button type="button" aria-label="分享"><Share2 size={16} /></button>
      <button type="button" aria-label="收藏"><Bookmark size={16} /></button>
      <button type="button" aria-label="更多"><MoreHorizontal size={16} /></button>
    </div>
  );
}

const THREADS = [
  { title: '跨时区会议时间协调', meta: '刚刚 · 6 条回答', active: true },
  { title: '季度用量与成本分析', meta: '2 小时前' },
  { title: '帮我起草发布邮件', meta: '昨天' },
  { title: '机票价格追踪设置', meta: '周二' },
  { title: '周报要点提炼', meta: '上周' },
];

const SUGGESTIONS = ['再对比一下伦敦', '导出为表格', '设个明早提醒'];

export default function LanguageGuiDemoPage() {
  return (
    <div className="lgui">
      <aside className="lgui-sidebar">
        <div className="lgui-brand">
          <span className="lgui-avatar" />
          <span className="lgui-brand-name">LanguageGUI</span>
        </div>
        <button type="button" className="lgui-new-chat">
          <Plus size={16} /> 新建对话
        </button>
        <div className="lgui-threads" role="list">
          <div className="lgui-nav-label">最近会话</div>
          {THREADS.map((t) => (
            <button
              key={t.title}
              type="button"
              role="listitem"
              className={`lgui-thread${t.active ? ' lgui-thread-active' : ''}`}
            >
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
            <span className="lgui-topbar-title">跨时区会议时间协调</span>
            <span className="lgui-model-chip">
              <Gauge size={12} /> gpt-4o · 富输出模式
            </span>
          </div>
          <div className="lgui-topbar-actions">
            <button type="button" className="lgui-icon-btn" aria-label="编辑标题"><Pencil size={15} /></button>
            <button type="button" className="lgui-icon-btn" aria-label="分享"><Share2 size={15} /></button>
            <button type="button" className="lgui-icon-btn" aria-label="设置"><Settings size={15} /></button>
          </div>
        </header>

        <div className="lgui-stream">
          <UserBubble name="Mauro Sicard" time="2:50 PM" text="现在旧金山、东京、柏林各几点了？我们约周会。" />

          <section className="lgui-turn">
            <AssistantHead time="2:50 PM" />
            <p className="lgui-prose">
              三地现在分别是——旧金山 <strong>周五下午</strong>、东京 <strong>周六清晨</strong>、柏林
              <strong>周五深夜</strong>。综合工作时间看，<strong>旧金山 8:00（柏林 17:00 / 东京次日 0:00 前后）</strong>
              是重叠窗口里对两方最友好的选择，另一备选是旧金山 20:00（东京 11:00）。
            </p>
            <ClockWidget />
            <ActionRow />
          </section>

          <UserBubble name="Mauro Sicard" time="2:52 PM" text="会费预算 $2,480.58，换成欧元是多少？" />

          <section className="lgui-turn">
            <AssistantHead time="2:52 PM" />
            <p className="lgui-prose">按今日中间市场汇率 0.90 换算：</p>
            <FxWidget />
            <ActionRow />
          </section>

          <section className="lgui-turn">
            <AssistantHead time="2:53 PM" />
            <p className="lgui-prose">顺带一提，本周团队的模型用量如下，deepseek 请求量最大但环比在回落：</p>
            <TableWidget />
          </section>

          <section className="lgui-turn">
            <AssistantHead time="2:53 PM" />
            <p className="lgui-prose">我把结论也录了一版语音版，通勤时可以听：</p>
            <VoiceWidget />
            <ActionRow />
          </section>

          <section className="lgui-turn">
            <AssistantHead time="2:54 PM" />
            <RatingWidget />
          </section>
        </div>

        <div className="lgui-composer-wrap">
          <div className="lgui-suggestions">
            {SUGGESTIONS.map((s) => (
              <button key={s} type="button" className="lgui-suggestion">{s}</button>
            ))}
          </div>
          <div className="lgui-composer">
            <input className="lgui-composer-input" placeholder="How can I help you?" aria-label="输入消息" />
            <div className="lgui-composer-bar">
              <div className="lgui-composer-tools">
                <button type="button" className="lgui-icon-btn" aria-label="附件"><Paperclip size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="语音输入"><Mic size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="图表"><BarChart3 size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="时钟"><Clock3 size={15} /></button>
                <button type="button" className="lgui-icon-btn" aria-label="会话"><MessageSquare size={15} /></button>
              </div>
              <button type="button" className="lgui-send" aria-label="发送">
                <Navigation size={16} />
              </button>
            </div>
          </div>
        </div>
      </main>
    </div>
  );
}
