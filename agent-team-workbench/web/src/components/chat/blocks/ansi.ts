// 终端输出 ANSI 展示基座：把带 SGR / 光标控制序列的命令输出解析成按行分组的
// span 数组，供块组件做高度截断后整行渲染。解析分三层：先剥掉不承载图形状态
// 的序列（OSC、非 CSI 转义、惰性控制符），再把 \r / \b / 擦行按终端列缓冲回放
// 成实际可见文本，最后把 SGR 流折叠成带颜色的文本 run。颜色模型覆盖 8/16 基础
// 色、256 色调色板与 truecolor，属性覆盖 bold/dim/italic/underline/strikethrough/
// hidden/reverse。

import type { CSSProperties } from 'react';

/** 一段终端文本；不携带 SGR 状态时 style 为 undefined，无需包装。 */
export interface AnsiSpan {
  text: string;
  style?: CSSProperties;
}

/** 一行输出的 span 序列，按顺序渲染。 */
export type AnsiLine = AnsiSpan[];

/** 解析器内部的一个文本 run 及其生效的图形状态。 */
interface AnsiChunk {
  content: string;
  /** 前景色，'r, g, b' 三元组；未设置时为 null。 */
  fg: string | null;
  /** 背景色，'r, g, b' 三元组；未设置时为 null。 */
  bg: string | null;
  /** 生效的 SGR 属性名（bold/dim/…），按声明顺序。 */
  decorations: readonly string[];
}

/**
 * 8/16 基础色的 rgb 三元组（惯用终端调色板：普通档 0/187/255，亮档 85/255）。
 * key 的空格形式与运行解析产出的颜色串一致，查 token 表时统一去空格。
 */
const BASIC_RGB: readonly (readonly string[])[] = [
  [
    '0, 0, 0', '187, 0, 0', '0, 187, 0', '187, 187, 0',
    '0, 0, 187', '187, 0, 187', '0, 187, 187', '255, 255, 255',
  ],
  [
    '85, 85, 85', '255, 85, 85', '0, 255, 0', '255, 255, 85',
    '85, 85, 255', '255, 85, 255', '85, 255, 255', '255, 255, 255',
  ],
];

/** 256 色调色板：0-15 复用基础色，16-231 是 6x6x6 立方体，232-255 是灰度阶梯。 */
function paletteColor(index: number): string {
  if (index < 16) return BASIC_RGB[index > 7 ? 1 : 0][index % 8];
  if (index < 232) {
    const levels = [0, 95, 135, 175, 215, 255];
    const cube = index - 16;
    const r = levels[Math.floor(cube / 36)];
    const g = levels[Math.floor((cube % 36) / 6)];
    const b = levels[cube % 6];
    return `${r}, ${g}, ${b}`;
  }
  const gray = 8 + (index - 232) * 10;
  return `${gray}, ${gray}, ${gray}`;
}

/**
 * 基础色到主题 token 的映射：黑/白都归正文色（否则会与所在表面同色不可读），
 * 亮黑取弱化文字色（灰角色），红绿黄蓝按语义对到状态色。magenta/cyan 没有
 * 对应 token，连同 256 色与 truecolor 一起落字面 rgb。
 */
const TOKEN_BY_BASIC_RGB: Record<string, string> = {
  '0,0,0': 'hsl(var(--color-text-primary))',
  '255,255,255': 'hsl(var(--color-text-primary))',
  '85,85,85': 'hsl(var(--color-text-tertiary))',
  '187,0,0': 'hsl(var(--color-status-error))',
  '255,85,85': 'hsl(var(--color-status-error))',
  '0,187,0': 'hsl(var(--color-status-success))',
  '0,255,0': 'hsl(var(--color-status-success))',
  '187,187,0': 'hsl(var(--color-status-warning))',
  '255,255,85': 'hsl(var(--color-status-warning))',
  '0,0,187': 'hsl(var(--color-status-info))',
  '85,85,255': 'hsl(var(--color-status-info))',
};

/**
 * 各 SGR 属性的 CSS。blink 故意缺席——不复现闪烁文本。reverse 不在此表：
 * 它在 run 产出时直接交换前景/背景。underline 与 strikethrough 共用
 * textDecoration，同 run 内两者都声明时后者覆盖前者。
 */
const STYLE_BY_DECORATION: Record<string, CSSProperties | undefined> = {
  bold: { fontWeight: 700 },
  dim: { opacity: 0.7 },
  italic: { fontStyle: 'italic' },
  underline: { textDecoration: 'underline' },
  strikethrough: { textDecoration: 'line-through' },
  hidden: { visibility: 'hidden' },
};

/** OSC 序列（窗口标题、超链接），带或不带终止符。 */
const OSC_SEQUENCE = /\u001b\][^\u0007\u001b]*(?:\u0007|\u001b\\)?/g;

/** CSI 之外的转义序列：字符集选择、单移、reset 等。 */
const NON_CSI_ESCAPE = /\u001b(?!\[)[\u0020-\u002f]*[\u0030-\u007e]?/g;

/** 完整 CSI 序列，分组：参数字节 / 中间字节 / final 字节。 */
const CSI_SEQUENCE = /\u001b\[([\u0030-\u003f]*)([\u0020-\u002f]*)([\u0040-\u007e])/g;

/**
 * 无显示含义的 C0 控制字符。\t、\n、\b、ESC 故意保留：前两个排版、\b 供列
 * 回放、ESC 供 CSI 切分。
 */
const INERT_CONTROL = /[\u0000-\u0007\u000b-\u001a\u001c-\u001f\u007f]/g;

/** stripAnsi 的控制字符集：比 INERT_CONTROL 多去掉 \b，只保留 \n 与 \t。 */
const STRIPPED_CONTROL = /[\u0000-\u0008\u000b-\u001a\u001c-\u001f\u007f]/g;

/**
 * 需要按列回放的行：含回车、退格或擦行序列。擦行的匹配形状与 replayLine
 * 解析的 CSI 形状一致（参数可带 `;` 与中间字节），保证 `\x1b[1;2K` 这类
 * 形式不会绕过守卫而漏掉自己的擦除。
 */
const NEEDS_REPLAY = /\r|\u0008|\u001b\[[\u0030-\u003f]*[\u0020-\u002f]*K/;

/** 仅 SGR 序列，用于无需回放的行折叠状态。 */
const SGR_SEQUENCE = /\u001b\[([\u0030-\u003f]*)[\u0020-\u002f]*m/g;

/** 终端制表位宽度：tab 推进到该宽度的下一个整倍数列。 */
const TAB_WIDTH = 8;

/**
 * 组合附加记号等零宽码点：终端不为它们推进列，`e` + U+0301 只占一格，
 * 双列重绘会把两个码点一起覆盖。
 */
const ZERO_WIDTH = /^[\p{Mn}\p{Me}\p{Cf}\u200b-\u200f\u2060]$/u;

/**
 * 终端按双列推进的字符：CJK 各文字、全角形式、CJK 标点与 emoji 呈现形式。
 * 文本呈现的符号（U+2600-U+27BF 里的 `\u2713`、`\u26a0` 等）是单列的，
 * 必须排除在外。
 */
const WIDE_CHAR = new RegExp(
  '\\p{Script=Han}|\\p{Script=Hiragana}|\\p{Script=Katakana}|\\p{Script=Hangul}'
  // 只取 emoji 呈现形式：U+2600-U+27BF 符号块大多是单列——每条进度行都
  // 会写的 `\u2713`（对勾）只推进一列（已对照真实终端验证），整块当作
  // 双列会让这张卡片存在的意义（列对齐）恰好失效。
  + '|\\p{Emoji_Presentation}'
  + '|[\\uff01-\\uff60\\u3000-\\u303e]',
  'u',
);

/** 一个字符是否占两个终端列（CJK、全角、emoji）。 */
function isWide(char: string): boolean {
  const code = char.codePointAt(0);
  if (code === undefined || code < 0x1100) return false;
  return WIDE_CHAR.test(char);
}

/**
 * 一格的图形状态，按字段规范化持有而非保留原始序列历史：终端跟踪的是
 * 「当前」状态而非转录，累积序列会让每个状态边界重发整条链，不带全 reset
 * 换色的输出会退化成 O(n^2) 个字符；同时 chalk 系工具写的属性关闭码
 * （39/49/22/23/24/27/29）才能真正关掉对应属性。
 */
interface SgrState {
  fg: string;
  bg: string;
  /** 生效中的属性参数码，如 `1`（bold）、`4`（underline）。 */
  attrs: readonly string[];
}

/** 默认状态：无颜色、无属性。 */
const SGR_NONE: SgrState = { fg: '', bg: '', attrs: [] };

/** 属性关闭码 → 各自关闭的开启参数码。 */
const ATTR_CLOSERS: Record<string, readonly string[]> = {
  22: ['1', '2'], 23: ['3'], 24: ['4'], 25: ['5', '6'], 27: ['7'], 28: ['8'], 29: ['9'],
};

/**
 * 把一条 SGR 序列的参数折叠进它产生的状态。
 * @param state 序列之前生效的状态。
 * @param params 序列的原始参数串（`31`、`1;4`、`38;5;208`）。
 */
function foldSgr(state: SgrState, params: string): SgrState {
  const codes = params === '' ? ['0'] : params.split(';');
  let next = state;
  for (let index = 0; index < codes.length; index++) {
    const code = String(codes[index]);
    if (code === '' || code === '0') {
      next = SGR_NONE;
      continue;
    }
    // 扩展色：`38;5;N` / `38;2;R;G;B` 与 `48` 背景对会消费自己的参数，整段取走。
    if (code === '38' || code === '48') {
      const kind = codes[index + 1] ?? '';
      const span = kind === '2' ? 4 : kind === '5' ? 2 : 0;
      const value = codes.slice(index, index + span + 1).join(';');
      next = code === '38' ? { ...next, fg: value } : { ...next, bg: value };
      index += span;
      continue;
    }
    const closes = ATTR_CLOSERS[code];
    if (closes !== undefined) {
      next = { ...next, attrs: next.attrs.filter((attr) => !closes.includes(attr)) };
      continue;
    }
    const numeric = Number(code);
    if (code === '39') {
      next = { ...next, fg: '' };
      continue;
    }
    if (code === '49') {
      next = { ...next, bg: '' };
      continue;
    }
    if ((numeric >= 30 && numeric <= 37) || (numeric >= 90 && numeric <= 97)) {
      next = { ...next, fg: code };
      continue;
    }
    if ((numeric >= 40 && numeric <= 47) || (numeric >= 100 && numeric <= 107)) {
      next = { ...next, bg: code };
      continue;
    }
    if (!next.attrs.includes(code)) next = { ...next, attrs: [...next.attrs, code] };
  }
  return next;
}

/** 把状态渲染成从默认态建立它的唯一规范序列；默认态返回空串。 */
function openSgr(state: SgrState): string {
  const codes = [...state.attrs];
  if (state.fg !== '') codes.push(state.fg);
  if (state.bg !== '') codes.push(state.bg);
  return codes.length === 0 ? '' : `\u001b[${codes.join(';')}m`;
}

/** 两个状态是否相同，只在变化处发出边界序列。 */
function sameSgr(a: SgrState, b: SgrState): boolean {
  return a.fg === b.fg && a.bg === b.bg && a.attrs.length === b.attrs.length
    && a.attrs.every((attr, index) => attr === b.attrs[index]);
}

/** 回放后的一列：写入它时生效的状态，与它的字符。 */
interface Cell {
  sgr: SgrState;
  char: string;
  /** 双列字符的第二列占位。 */
  spacer?: boolean;
}

/**
 * 按终端绘制方式把一行的光标移动回放进列缓冲。回车与退格只移动光标、
 * 不擦除任何内容，读者看到的是每列最后一次被写入的内容——这一区别正是
 * 用列缓冲而非字符串手术的原因：`100%\rOK` 显示 `OK0%`，因为重绘比底下
 * 那帧短；结尾的 `abc\b` 仍显示 `abc`，因为没有字符覆盖过 `c`。
 *
 * CSI 序列不占列，它改变后续写入所带的状态（终端正是按格存颜色的），所以
 * 「红色 bad」接三个退格再写 `ok` 显示的是红色的 `okd`：`ok` 覆盖了两格，
 * 第三格保留它被写入时的红色。列最终按 run 重发，解析层看到的就是同样的
 * 样式。
 * @param line 仍带 CSI 序列的一行输出。
 * @param entrySgr 行首进入时生效的 SGR 状态（换行不重置它）。
 * @returns 终端回放完毕后的行文本，及行尾的 SGR 状态（供下一行进入）。
 */
function replayLine(line: string, entrySgr: SgrState): { text: string; sgr: SgrState } {
  // 与解析层相同的切分形状，序列在此也是一个整体。
  const csi = /\u001b\[([\u0030-\u003f]*)[\u0020-\u002f]*([\u0040-\u007e])/g;
  const columns: (Cell | undefined)[] = [];
  let cursor = 0;
  // 状态按终端的方式跟踪：每格盖上写入那一刻生效的状态，之后的重绘无法
  // 重新样式化它没碰到的格。行首带着上一行的状态进入，因为换行不重置。
  let sgr = entrySgr;
  let at = 0;

  /** 擦掉一格；若是双列字符的一半，另一列也一并清（终端成对擦除）。 */
  const clear = (index: number, fill: string): void => {
    const cell = columns[index];
    if (cell?.spacer === true && index > 0) columns[index - 1] = { sgr, char: fill };
    else if (cell !== undefined && isWide(cell.char) && columns[index + 1]?.spacer === true) {
      columns[index + 1] = { sgr, char: fill };
    }
    columns[index] = { sgr, char: fill };
  };

  const consume = (chunk: string): void => {
    for (const char of chunk) {
      if (char === '\r') {
        cursor = 0;
        continue;
      }
      if (char === '\u0008') {
        cursor = Math.max(0, cursor - 1);
        continue;
      }
      if (char === '\t') {
        // tab 推进到下一个 8 列制表位，跳过的格保持原样——这正是重绘后
        // 制表列还能幸存的原因，而列对齐是这张卡片存在的全部意义。
        const stop = cursor + TAB_WIDTH - (cursor % TAB_WIDTH);
        for (; cursor < stop; cursor++) columns[cursor] ??= { sgr, char: ' ' };
        continue;
      }
      if (ZERO_WIDTH.test(char)) {
        // 零宽字符不占自己的列：附着到已写入的格上，覆盖该格即覆盖记号。
        // 没有可附着的格（行首、或重绘到第 0 列之后）时终端什么都不显示。
        const base = cursor > 0 ? columns[cursor - 1] : undefined;
        if (base !== undefined) columns[cursor - 1] = { sgr: base.sgr, char: base.char + char };
        continue;
      }
      // 写到双列字符的任一半都会让另一半失效：终端不能留下双列字形的一半。
      clear(cursor, ' ');
      columns[cursor] = { sgr, char };
      cursor++;
      // 双列字符占两列，尾列标记为占位：首列被覆盖时留下空格而不是合缝左移。
      if (isWide(char)) {
        columns[cursor] = { sgr, char: '', spacer: true };
        cursor++;
      }
    }
  };

  for (const match of line.matchAll(csi)) {
    consume(line.slice(at, match.index));
    at = match.index + match[0].length;
    const params = match[1];
    const final = match[2];
    if (final === 'K') {
      // 擦行：\r 在每个 spinner 与进度条里的固定搭档。没有它，更短的重绘
      // 会留下上一帧的尾巴——那是终端从未显示过的文本。模式 `1` 从行首
      // 擦到光标列（按 CSI 规范含光标列）而不是丢弃这些格：光标不动，之后
      // 的写入仍可能落在它们之后。只有第一个参数选择模式，其余忽略
      // （`1;2K` 与 `1K` 擦得一样）。
      const mode = params.split(';')[0];
      if (mode === '1') for (let index = 0; index <= cursor; index++) clear(index, ' ');
      else columns.length = mode === '2' ? 0 : cursor;
      continue;
    }
    // 只有 SGR 承载图形状态；其余 final 字节是光标/擦除动作，不得影响格的样式。
    if (final !== 'm') continue;
    sgr = foldSgr(sgr, params);
  }
  consume(line.slice(at));

  // 重发各列，只在状态变化处开启 run。每个边界发出的是建立该状态的那一条
  // 规范序列，这保证输出规模随格数线性、与到达路径无关。
  let out = '';
  let active = entrySgr;
  for (let index = 0; index < columns.length; index++) {
    const column = columns[index] ?? { sgr: SGR_NONE, char: ' ' };
    if (!sameSgr(column.sgr, active)) {
      if (!sameSgr(active, SGR_NONE)) out += '\u001b[0m';
      out += openSgr(column.sgr);
      active = column.sgr;
    }
    // 占位格仍持有自己的列：首列存活时双列字形横跨两列、占位格不发出字符；
    // 首列被后来的写入替换后，终端把占位格清成空格而不是合缝——不发出
    // 字符会让它后面的所有内容左移一列。
    const leadIntact = index > 0 && isWide(columns[index - 1]?.char ?? '');
    out += column.spacer === true && !leadIntact ? ' ' : column.char;
  }
  // 收敛到扫描结束时（而非最后一个写入格的）状态：最后一次写入之后的序列
  // （比如收尾彩色行的那条 `\x1b[0m`）不改变任何格，却终结了 run，必须同时
  // 传给后续行与输出。少了这一步，以 reset 结尾的行会把颜色泄漏给下一行。
  if (!sameSgr(active, sgr)) {
    if (!sameSgr(active, SGR_NONE)) out += '\u001b[0m';
    out += openSgr(sgr);
  }
  return { text: out, sgr };
}

/**
 * 回放每一行的光标移动。只终止 CRLF 行的 `\r` 先去掉，这些行保有自己的
 * 文本而不是被重绘到自身上。SGR 状态跨行传递：换行不重置它，重绘前开启
 * 的 run 会继续给它后面的行上色。
 */
function applyCursorMovements(text: string): string {
  const replayed: string[] = [];
  let sgr = SGR_NONE;
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\r+$/, '');
    if (NEEDS_REPLAY.test(line)) {
      const result = replayLine(line, sgr);
      replayed.push(result.text);
      sgr = result.sgr;
      continue;
    }
    // 无光标移动的行不需要列缓冲——为它按字符分配格等于为从不重绘的输出
    // （`ls -R`、五千行日志）白付一格一字符的成本。只需折叠它自己的 SGR，
    // 让之后真正要回放的行带着正确的状态进入。
    replayed.push(line);
    for (const match of line.matchAll(SGR_SEQUENCE)) sgr = foldSgr(sgr, match[1]);
  }
  return replayed.join('\n');
}

/**
 * 去掉所有不承载颜色的转义序列与控制字符，留下 CSI 序列给 SGR 解析、
 * `\n` / `\t` 给排版。光标移动（回车、退格）先回放：它们对可见文本的影响
 * 必须先落定，表达它们的字符才能被丢弃。
 */
function sanitize(text: string): string {
  const escaped = text.replace(OSC_SEQUENCE, '').replace(NON_CSI_ESCAPE, '');
  return applyCursorMovements(escaped).replace(INERT_CONTROL, '');
}

/** SGR run 解析器的图形状态：颜色已解析为 rgb 三元组。 */
interface RunState {
  fg: string | null;
  bg: string | null;
  decorations: string[];
}

const RUN_NONE: RunState = { fg: null, bg: null, decorations: [] };

/**
 * 把一条 SGR 参数串折叠进 run 状态，语义对齐终端惯例：0 或非法参数全重置，
 * 21-29 关闭对应属性，39/49 恢复默认前景/背景，30-37/90-97 与 40-47/100-107
 * 是基础与亮色，38/48 承接 256 色与 truecolor 的扩展参数（参数不足时静默忽略）。
 */
function foldRun(state: RunState, params: string): RunState {
  const nums = params.split(';');
  let { fg, bg } = state;
  const decorations = [...state.decorations];
  const remove = (name: string): void => {
    const at = decorations.indexOf(name);
    if (at >= 0) decorations.splice(at, 1);
  };
  while (nums.length > 0) {
    const raw = nums.shift() ?? '';
    const num = Number.parseInt(raw, 10);
    if (Number.isNaN(num) || num === 0) {
      fg = null;
      bg = null;
      decorations.length = 0;
    } else if (num === 1) decorations.push('bold');
    else if (num === 2) decorations.push('dim');
    else if (num === 3) decorations.push('italic');
    else if (num === 4) decorations.push('underline');
    else if (num === 5) decorations.push('blink');
    else if (num === 7) decorations.push('reverse');
    else if (num === 8) decorations.push('hidden');
    else if (num === 9) decorations.push('strikethrough');
    else if (num === 21) remove('bold');
    else if (num === 22) {
      remove('bold');
      remove('dim');
    } else if (num === 23) remove('italic');
    else if (num === 24) remove('underline');
    else if (num === 25) remove('blink');
    else if (num === 27) remove('reverse');
    else if (num === 28) remove('hidden');
    else if (num === 29) remove('strikethrough');
    else if (num === 39) fg = null;
    else if (num === 49) bg = null;
    else if (num >= 30 && num <= 37) fg = BASIC_RGB[0][num % 10];
    else if (num >= 90 && num <= 97) fg = BASIC_RGB[1][num % 10];
    else if (num >= 40 && num <= 47) bg = BASIC_RGB[0][num % 10];
    else if (num >= 100 && num <= 107) bg = BASIC_RGB[1][num % 10];
    else if (num === 38 || num === 48) {
      const mode = nums.shift();
      if (mode === '5') {
        const index = Number.parseInt(nums.shift() ?? '', 10);
        if (index >= 0 && index <= 255) {
          if (num === 38) fg = paletteColor(index);
          else bg = paletteColor(index);
        }
      } else if (mode === '2' && nums.length >= 3) {
        const r = Number.parseInt(nums.shift() ?? '', 10);
        const g = Number.parseInt(nums.shift() ?? '', 10);
        const b = Number.parseInt(nums.shift() ?? '', 10);
        if (r >= 0 && r <= 255 && g >= 0 && g <= 255 && b >= 0 && b <= 255) {
          const color = `${r}, ${g}, ${b}`;
          if (num === 38) fg = color;
          else bg = color;
        }
      }
    }
  }
  return { fg, bg, decorations };
}

/** 产出 chunk 时应用 reverse：缺省前景补白、背景补黑后互换（黑字白底）。 */
function toChunk(content: string, state: RunState): AnsiChunk {
  let { fg, bg } = state;
  const decorations = state.decorations.filter((decoration) => decoration !== 'reverse');
  if (state.decorations.includes('reverse')) {
    if (fg === null) fg = BASIC_RGB[0][7];
    if (bg === null) bg = BASIC_RGB[0][0];
    [fg, bg] = [bg, fg];
  }
  return { content, fg, bg, decorations };
}

/**
 * 把 sanitize 后（只剩 CSI 序列）的文本切成带图形状态的 run：SGR 序列折叠
 * 状态，其余 CSI（光标动作的残留）剥掉不占文本。
 */
function parseChunks(text: string): AnsiChunk[] {
  const chunks: AnsiChunk[] = [];
  let state = RUN_NONE;
  let at = 0;
  for (const match of text.matchAll(CSI_SEQUENCE)) {
    const plain = text.slice(at, match.index);
    if (plain !== '') chunks.push(toChunk(plain, state));
    at = match.index + match[0].length;
    // 只有 SGR（final 为 m）改变图形状态；其余 CSI 是光标动作，剥掉即可。
    if (match[3] === 'm') state = foldRun(state, match[1]);
  }
  const rest = text.slice(at);
  if (rest !== '') chunks.push(toChunk(rest, state));
  return chunks;
}

/**
 * 解析一个 run 的颜色与装饰为内联样式。
 * @returns 无 SGR 状态时返回 undefined。
 */
function resolveStyle(chunk: AnsiChunk): CSSProperties | undefined {
  const style: CSSProperties = {};
  const background = chunk.bg === null ? undefined : `rgb(${chunk.bg})`;
  if (background !== undefined) style.backgroundColor = background;
  if (chunk.fg !== null) {
    const literal = `rgb(${chunk.fg})`;
    // 自带背景的 run 保留字面前/背景对，作者定的对比度优先；只有前景的
    // run 映射到主题 token，随表面配色自适应。
    style.color = background === undefined
      ? TOKEN_BY_BASIC_RGB[chunk.fg.replace(/\s+/g, '')] ?? literal
      : literal;
  }
  for (const decoration of chunk.decorations) Object.assign(style, STYLE_BY_DECORATION[decoration]);
  return Object.keys(style).length === 0 ? undefined : style;
}

/**
 * 把命令输出解析成按行分组的带样式 span。
 * @param text 原始输出，可含 ANSI 转义序列。
 * @returns 每行一个条目（至少一条，可能是空行）。
 */
export function parseAnsiLines(text: string): AnsiLine[] {
  let current: AnsiSpan[] = [];
  const lines: AnsiSpan[][] = [current];
  for (const chunk of parseChunks(sanitize(text))) {
    const style = resolveStyle(chunk);
    for (const [index, part] of chunk.content.split('\n').entries()) {
      if (index > 0) {
        current = [];
        lines.push(current);
      }
      if (part !== '') current.push({ text: part, style });
    }
  }
  return lines;
}

/** 去掉全部 ANSI 转义与控制字符、只保留 \n 与 \t 的纯文本。 */
export function stripAnsi(text: string): string {
  return text
    .replace(OSC_SEQUENCE, '')
    .replace(NON_CSI_ESCAPE, '')
    .replace(CSI_SEQUENCE, '')
    .replace(STRIPPED_CONTROL, '');
}
