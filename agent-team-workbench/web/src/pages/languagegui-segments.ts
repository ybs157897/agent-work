/**
 * LanguageGUI 部件协议解析（演示支线）。
 * 模型按系统提示输出「普通文本 + ```lgui 围栏 JSON 块」——每个闭合块是一个
 * widget 对象（{"widget":"clock",...}），前端把流式全文切成可渲染段。
 * 纯函数、增量安全：未闭合的围栏返回 pending 段（渲染生成中占位），
 * 闭合但 JSON 非法降级为 broken 段（原样代码卡），绝不吞内容。
 */

export interface LguiWidgetData {
  widget: string;
  [key: string]: unknown;
}

export type LguiSegment =
  | { kind: 'text'; text: string }
  | { kind: 'widget'; data: LguiWidgetData }
  | { kind: 'pending' }
  | { kind: 'broken'; raw: string };

const FENCE = '```lgui';
const CLOSE = '```';

/** 从 contentStart 起找行首闭合 ```；返回内容终点与闭合后位置，未闭合返回 null。 */
function findClose(full: string, contentStart: number): { end: number; after: number } | null {
  for (let i = contentStart; i + 2 < full.length; i++) {
    if (full[i] !== '`' || (i > 0 && full[i - 1] !== '\n')) continue;
    if (full.slice(i, i + 3) !== CLOSE) continue;
    let after = i + 3;
    if (full[after] === '\r') after++;
    if (full[after] === '\n') after++;
    return { end: i, after };
  }
  return null;
}

export function parseSegments(full: string): LguiSegment[] {
  const segments: LguiSegment[] = [];
  let cursor = 0;
  for (;;) {
    const open = full.indexOf(FENCE, cursor);
    if (open < 0) break;
    const before = full.slice(cursor, open);
    if (before.trim()) segments.push({ kind: 'text', text: before.trim() });
    const lineEnd = full.indexOf('\n', open);
    if (lineEnd < 0) {
      segments.push({ kind: 'pending' }); // 围栏行尚未输出完
      return segments;
    }
    const contentStart = lineEnd + 1;
    const close = findClose(full, contentStart);
    if (!close) {
      segments.push({ kind: 'pending' }); // 块内容流式生成中
      return segments;
    }
    const raw = full.slice(contentStart, close.end).trim();
    try {
      const data = JSON.parse(raw) as LguiWidgetData;
      if (data && typeof data.widget === 'string') segments.push({ kind: 'widget', data });
      else segments.push({ kind: 'broken', raw });
    } catch {
      segments.push({ kind: 'broken', raw });
    }
    cursor = close.after;
  }
  const tail = full.slice(cursor);
  if (tail.trim()) segments.push({ kind: 'text', text: tail.trim() });
  return segments;
}

/** 「2:40 PM」→ 钟面指针角度输入；解析失败回退 12:00。 */
export function parseClockTime(time: string): { hour: number; minute: number } {
  const m = time.match(/(\d{1,2})\s*:\s*(\d{2})\s*([AP]\.?M\.?)/i);
  if (!m) return { hour: 12, minute: 0 };
  let hour = Number(m[1]) % 12;
  if (/p/i.test(m[3])) hour += 12;
  return { hour, minute: Number(m[2]) };
}

/** 币种 → 旗帜 emoji（演示用近似）；未知回退地球。 */
const FLAGS: Record<string, string> = {
  USD: '🇺🇸', EUR: '🇪🇺', CNY: '🇨', JPY: '🇵', GBP: '🇬🇧',
  KRW: '🇰🇷', HKD: '🇭🇰', TWD: '🇹', SGD: '🇸🇬', AUD: '🇦🇺',
};
export function currencyFlag(code: string): string {
  return FLAGS[code?.toUpperCase()] ?? '🌐';
}

/** 演示页系统提示：LanguageGUI 部件契约（文本 + ```lgui JSON 块）。 */
export const SYSTEM_PROMPT = [
  '你是 LanguageGUI——一个把回答渲染成结构化界面的 AI。',
  '回复格式：普通中文文本段落，中间可插入 ```lgui 围栏代码块；每个块内恰好一个 JSON 对象，代表一张 widget 卡片。可用 widget：',
  '{"widget":"clock","cities":[{"city":"城市","tz":"时区缩写","time":"2:40 PM","date":"Friday, Jan 19","active":true}]} —— 多城市时间，active 至多一个',
  '{"widget":"fx","from":"USD","to":"EUR","amount":"$2,480.58","rate":"1 USD = 0.90 EUR","result":"€2,232.52"} —— 货币换算',
  '{"widget":"table","title":"标题","columns":["列1","列2"],"rows":[["a","b"],["c",{"text":"+12%","tone":"up"}]]} —— 结构化数据；单元格可为字符串或 {"text","tone":"up|down"}',
  '{"widget":"chart","title":"标题","labels":["一","二"],"values":[3,5],"unit":"M"} —— 数值对比柱状图',
  '{"widget":"rating","question":"这次回答对你有帮助吗？"} —— 结尾反馈（可选）',
  '规则：JSON 必须可直接解析（双引号、无注释、无尾逗号）；只在结构化确实优于散文时用 widget；你没有实时数据，涉及当前时间/汇率时给出合理估算并在正文注明是演示数据；文本在前、widget 紧随其说明。',
].join('\n');
