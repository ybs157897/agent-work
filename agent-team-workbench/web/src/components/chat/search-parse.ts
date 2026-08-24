import type { SearchFileGroup } from './blocks/SearchBlock';

/**
 * `path:line:content` 形态（grep 默认输出）。path 用惰性匹配回溯到「冒号+数字+分隔符」处，
 * Windows 盘符（`C:\...`、`C:/...`）内建的盘符冒号不构成歧义；行号后的分隔兼容半角/全角冒号。
 */
const GREP_LINE = /^(.+?):(\d+)[：:](.*)$/;

/** 工具输出统一按 2000 字符截断；达到该长度即认为末行可能不完整（简化判定规则）。 */
const TRUNCATED_HINT_LENGTH = 1900;

/**
 * 把 grep 风格的纯文本输出解析为 SearchBlock 的 matches 形态数据。
 * 防误判门槛：至少 2 行命中，且命中行数 ≥ 非空总行数的 60%，否则视为普通文本返回 null
 * （分母取非空行：grep 输出的空行只是噪声，尾随换行不该稀释占比）。
 * 末行可能被 2000 字符截断切断——照常解析，不做特殊处理。
 * @param text 工具输出的纯文本。
 * @returns 按首见顺序分组的文件命中（非连续同 path 合并回首见组）与截断标记；不像 grep 输出时 null。
 */
export function parseGrepOutput(text: string): { files: SearchFileGroup[]; truncated: boolean } | null {
  const nonEmpty = text.split('\n').filter((line) => line !== '');
  if (nonEmpty.length === 0) return null;
  const groups = new Map<string, SearchFileGroup>();
  let hits = 0;
  for (const line of nonEmpty) {
    const match = GREP_LINE.exec(line);
    if (match === null) continue;
    hits += 1;
    const entry = { lineNumber: Number(match[2]), line: match[3] };
    const group = groups.get(match[1]);
    if (group) group.matches.push(entry);
    else groups.set(match[1], { path: match[1], matches: [entry] });
  }
  if (hits < 2 || hits / nonEmpty.length < 0.6) return null;
  return { files: [...groups.values()], truncated: text.length >= TRUNCATED_HINT_LENGTH };
}

/**
 * 把按行排列的纯文本解析为路径列表（SearchBlock 的 paths 形态数据）。
 * 每行必须非空、不以空白开头、且「像路径」（含 / 或 \ 分隔符，或以 .ext 扩展名结尾）；
 * 容忍单个尾随换行产生的末尾空行，其余任何违例整体判 null，避免把普通段落误判成路径列表。
 * @param text 工具输出的纯文本。
 * @returns 路径行数组；不像路径列表时 null。
 */
export function parsePathList(text: string): string[] | null {
  const body = text.endsWith('\n') ? text.slice(0, -1) : text;
  if (body === '') return null;
  const lines = body.split('\n');
  for (const line of lines) {
    if (line === '' || /^\s/.test(line)) return null;
    if (!/[\\/]/.test(line) && !/\.[A-Za-z0-9]+$/.test(line)) return null;
  }
  return lines;
}
