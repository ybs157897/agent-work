/**
 * 正文里的完整交付文档投影。
 *
 * 只做“识别与切分”，不做 Markdown 重写：返回的每一段都是原文切片，
 * `introMarkdown + documentMarkdown === markdown`。识别是保守的——第一个
 * 自己起块的顶层 ATX 一级标题被当作文档起点，且要求其后至少两个二至六级
 * 顶层小节、文档本体不短于最小长度、前置解释比文档本体短，才认为这是一篇
 * 被正文携带的完整文档。普通短答、只有小标题的回答、正文里的示例代码
 * （围栏内的 `#`）一律不触发。
 *
 * 扫描器只跟踪围栏代码块与顶层 ATX 标题，不引入第二套 Markdown 渲染/剥离；
 * 遇到歧义（缩进标题、被段落直接打断的标题、setext 标题、没有一级标题）
 * 直接放弃识别，让调用方维持原来的整篇正文直出。
 */

export interface DeliveredDocumentProjection {
  /** H1 标题文本（去掉行首井号与 CommonMark 结尾井号），未做其他 Markdown 清洗。 */
  title: string;
  /** H1 之前的原文（含空行与分隔线）；没有前置正文时为 ''。 */
  introMarkdown: string;
  /** 从 H1 行起至正文末尾的原文（含文后解释与围栏代码，不做二次切分）。 */
  documentMarkdown: string;
}

/** 文档本体最小长度：低于此值的回答不足以被当作交付文档。 */
const MIN_DOCUMENT_LENGTH = 800;
/** H1 之后最少的小节数（h2..h6）。 */
const MIN_SECTIONS = 2;
/** 下载文件名保留的标题长度上限（不含扩展名）。 */
const DOWNLOAD_NAME_LIMIT = 60;
/** 文件名里会破坏下载/路径语义的字符：控制符与各平台保留字符。 */
const UNSAFE_FILENAME_CHARS = /[\u0000-\u001f\u007f<>:"/\\|?*]/g;
/** 主题分隔线：`---` / `***` / `___`（允许中间空格）。 */
const THEMATIC_BREAK = /^[ \t]{0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$/;

interface TopLevelHeading {
  /** 标题行在原文中的起始偏移。 */
  start: number;
  level: number;
  title: string;
}

interface FenceState {
  marker: '`' | '~';
  length: number;
}

function openingFence(line: string): FenceState | undefined {
  const match = line.match(/^ {0,3}(`{3,}|~{3,})(.*)$/);
  if (!match) return undefined;
  const marker = match[1][0] as '`' | '~';
  // CommonMark：反引号 fence 的 info string 不允许再出现反引号。
  if (marker === '`' && match[2].includes('`')) return undefined;
  return { marker, length: match[1].length };
}

function closingFence(line: string, fence: FenceState): boolean {
  const match = line.match(/^ {0,3}([`~]+)[ \t]*$/);
  return !!match && match[1][0] === fence.marker && match[1].length >= fence.length;
}

function headingOf(line: string): { level: number; title: string } | undefined {
  // 列 0 的 ATX 标题：缩进标题可能属于列表项内容或缩进代码块，直接跳过。
  const match = line.match(/^(#{1,6})(?:[ \t]+(.*?))?[ \t]*$/);
  if (!match) return undefined;
  const title = (match[2] ?? '').replace(/[ \t]+#+[ \t]*$/, '').trim();
  return { level: match[1].length, title };
}

function scanTopLevelHeadings(markdown: string): TopLevelHeading[] {
  const headings: TopLevelHeading[] = [];
  let offset = 0;
  let fence: FenceState | undefined;
  while (offset < markdown.length) {
    const newline = markdown.indexOf('\n', offset);
    const lineEnd = newline === -1 ? markdown.length : newline;
    const raw = markdown.slice(offset, lineEnd);
    const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw;

    if (fence) {
      if (closingFence(line, fence)) fence = undefined;
    } else {
      const opened = openingFence(line);
      if (opened) {
        fence = opened;
      } else {
        const heading = headingOf(line);
        if (heading) headings.push({ start: offset, level: heading.level, title: heading.title });
      }
    }

    if (lineEnd >= markdown.length) break;
    offset = lineEnd + 1;
  }
  return headings;
}

/**
 * 标题必须自己起一个块：位于文本开头，或前面是一行空白/主题分隔线。
 * 直接紧贴在普通段落文字后面的 `#` 行按歧义处理（CommonMark 里它虽然是标题，
 * 但那更像正文里引用的一行，不足以当文档边界）。
 */
function startsAtBlockBoundary(markdown: string, start: number): boolean {
  if (start === 0) return true;
  // 行首偏移的前一个字符一定是换行符；CRLF 下也是（`\n` 在 `\r` 之后）。
  if (markdown[start - 1] !== '\n') return false;
  const previousNewline = markdown.lastIndexOf('\n', start - 2);
  const previousLine = markdown.slice(previousNewline + 1, start - 1).replace(/\r$/, '');
  return previousLine.trim() === '' || THEMATIC_BREAK.test(previousLine);
}

/**
 * 识别正文携带的完整文档并做纯原文切分。返回 null 表示维持整篇 Markdown 直出。
 */
export function projectDeliveredDocument(markdown: string): DeliveredDocumentProjection | null {
  if (markdown.length < MIN_DOCUMENT_LENGTH) return null;

  const headings = scanTopLevelHeadings(markdown);
  // 前言里可以有小标题（甚至更早的一级标题被当成引用）；文档起点取第一个
  // 自己起块、且带标题文本的一级标题。找不到就整篇直出。
  const titleHeading = headings.find(
    (heading) => heading.level === 1 && heading.title.length > 0 && startsAtBlockBoundary(markdown, heading.start),
  );
  if (!titleHeading) return null;

  const documentMarkdown = markdown.slice(titleHeading.start);
  if (documentMarkdown.length < MIN_DOCUMENT_LENGTH) return null;

  const sectionCount = headings.filter(
    (heading) => heading.start > titleHeading.start && heading.level >= 2,
  ).length;
  if (sectionCount < MIN_SECTIONS) return null;

  const introMarkdown = markdown.slice(0, titleHeading.start);
  // 文档应当是这条消息的主体；前置解释比文档还长时不做阅读面切换，避免误判长讨论。
  if (introMarkdown.length >= documentMarkdown.length) return null;

  return {
    title: titleHeading.title,
    introMarkdown,
    documentMarkdown,
  };
}

/**
 * 交付文档的 `.md` 下载文件名：标题里的路径保留字符与控制符换成空格，
 * 去首尾点和空白，超长截断；标题不可用时退回 `document.md`。
 */
export function documentDownloadName(title: string): string {
  const cleaned = title
    .replace(UNSAFE_FILENAME_CHARS, ' ')
    .replace(/\s+/g, ' ')
    .replace(/^[.\s]+/, '')
    .slice(0, DOWNLOAD_NAME_LIMIT)
    .replace(/[.\s]+$/, '');
  return cleaned ? `${cleaned}.md` : 'document.md';
}
