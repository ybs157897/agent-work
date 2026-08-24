import type { ReadBlockProps } from './blocks/ReadBlock';

/** 扩展名 → highlight.js 语言 id；键统一小写（查找前对路径做小写归一）。 */
const EXT_LANG: Readonly<Record<string, string | undefined>> = {
  ts: 'typescript',
  tsx: 'typescript',
  js: 'javascript',
  jsx: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  py: 'python',
  go: 'go',
  rs: 'rust',
  java: 'java',
  c: 'c',
  h: 'c',
  cc: 'cpp',
  cpp: 'cpp',
  hpp: 'cpp',
  css: 'css',
  scss: 'scss',
  html: 'xml',
  vue: 'xml',
  json: 'json',
  yaml: 'yaml',
  yml: 'yaml',
  md: 'markdown',
  sh: 'bash',
  bash: 'bash',
  zsh: 'bash',
  sql: 'sql',
  toml: 'ini',
  xml: 'xml',
  rb: 'ruby',
  php: 'php',
  swift: 'swift',
  kt: 'kotlin',
  lua: 'lua',
  vim: 'vim',
  dockerfile: 'dockerfile',
  makefile: 'makefile',
};

/** 无扩展名时按 basename（小写）精确匹配的特例文件。 */
const BASENAME_LANG: Readonly<Record<string, string | undefined>> = {
  dockerfile: 'dockerfile',
  makefile: 'makefile',
};

/**
 * 由文件路径推断 highlight.js 语言 id（ReadBlock 的 lang 提示）。
 * 扩展名大小写不敏感；无扩展名（含点前导文件如 .gitignore——点在首位不算扩展名）
 * 时按 basename 精确匹配 dockerfile/makefile；未命中返回 undefined（纯等宽渲染）。
 * @param path 文件路径（basename 取自最后的 / 或 \ 分隔符）。
 * @returns 语言 id，或 undefined。
 */
export function langFromPath(path: string): string | undefined {
  const base = path.trim().toLowerCase().split(/[\\/]/).pop();
  if (base === undefined || base === '') return undefined;
  const dot = base.lastIndexOf('.');
  if (dot > 0) return EXT_LANG[base.slice(dot + 1)];
  return BASENAME_LANG[base];
}

/**
 * 把 read 类工具的纯文本输出适配成 ReadBlock 的 props。
 * 行号一律从 1 起（我们的 read 输出不携带 offset 信息）；无窗口信息故不传 totalLines。
 * @param detail 工具输出的文件内容（\r\n / 裸 \r 归一为 \n）。
 * @param filePath 文件路径（label 与语言推断依据），可缺省。
 * @returns ReadBlock props；detail 去空白后为空时 null。
 */
export function readBlockFromOutput(detail: string, filePath?: string): ReadBlockProps | null {
  const trimmed = detail.trim();
  if (trimmed === '') return null;
  const rows = trimmed.replace(/\r\n?/g, '\n').split('\n');
  return {
    label: filePath,
    lines: rows.map((text, index) => ({ number: index + 1, text })),
    lang: langFromPath(filePath ?? ''),
  };
}
