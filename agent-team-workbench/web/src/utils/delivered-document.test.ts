import { describe, expect, it } from 'vitest';
import { documentDownloadName, projectDeliveredDocument } from './delivered-document';

/** 前言（含结论先行与分隔线）与真实样例同形，体积压到最小。 */
const INTRO = [
  '我先摸清仓库里“任务看板”和搜索相关的现状，再分派子 agent 分片起草，已存在 `task-search.store.ts` 等实现痕迹。',
  '**结论先行**：在册入口只有一个本地过滤框；下面 12 条验收条件各给出判定方法与反例。',
  '',
  '---',
  '',
  '',
].join('\n');

function acceptanceCriterion(index: number): string {
  const id = String(index).padStart(2, '0');
  return [
    `- [ ] **AC-${id} 命中必现**：输入词与任务标题做大小写不敏感子串匹配，命中项**应当**保留在看板中。`,
    '',
    `  判定：构造标题含唯一词 AC-${id} 的根任务，输入该词，断言卡片仍在且列头计数含它。反例：卡片消失。`,
    '',
  ].join('\n');
}

/** H1 + 5 个 H2 + 12 条 AC + 末尾 languagegui 表格，对应线上真实正文形态。 */
function acceptanceDocument(): string {
  return [
    '# 《任务看板标题搜索》验收条件',
    '',
    '日期：2026-09-13。状态：草案（待产品裁决 §待裁决）。基线：`main` 工作树只读核对，未改代码。',
    '',
    '## 适用范围',
    '',
    '- 入口：`/tasks` 头部搜索框（placeholder「搜索任务或 WI 编号」，含「清空搜索」按钮）。',
    '- 数据源：根任务投影 `rootItems`，非派生任务全量。',
    '',
    '## 验收条件',
    '',
    ...Array.from({ length: 12 }, (_, index) => acceptanceCriterion(index + 1)),
    '## 待裁决',
    '',
    '- 是否保留 FTS 检索面板：需要产品裁决。',
    '',
    '## 非目标',
    '',
    '- 不覆盖第二套解析器。',
    '',
    '## 判定环境与证据要求',
    '',
    '- 现有测试只覆盖本地过滤，AC-05~AC-12 零覆盖，需先补可断言的测试面。',
    '',
    '```languagegui',
    '{"version":"languagegui/v1","blocks":[{"type":"table","title":"看板可见集合的三处计数口径",'
      + '"columns":[{"key":"loc","label":"位置"}],"rows":[{"loc":"工具栏计数","rule":"等于当前可见根任务数"}]}]}',
    '```',
    '',
  ].join('\n');
}

/** 前言 + 两小节的完整文档；filler 控制文档体量。 */
function sectioned(prefix = '', filler = 300): string {
  return [
    prefix,
    '# 《示例》验收条件',
    '',
    '## 第一节',
    '',
    '正文段落。'.repeat(filler),
    '',
    '## 第二节',
    '',
    '补充说明。'.repeat(filler),
    '',
  ].join('\n');
}

describe('projectDeliveredDocument · 真实样例形态', () => {
  const markdown = INTRO + acceptanceDocument();
  const projection = projectDeliveredDocument(markdown);

  it('识别出正文携带的完整文档并保留标题', () => {
    expect(projection).not.toBeNull();
    expect(projection?.title).toBe('《任务看板标题搜索》验收条件');
  });

  it('只按原文切分：前言 + 文档逐字等于原文，无丢失无重复', () => {
    expect(projection?.introMarkdown).toBe(INTRO);
    expect(projection?.documentMarkdown).toBe(acceptanceDocument());
    expect((projection?.introMarkdown ?? '') + (projection?.documentMarkdown ?? '')).toBe(markdown);
    expect(projection?.documentMarkdown.startsWith('# 《任务看板标题搜索》验收条件')).toBe(true);
  });

  it('12 条 AC、5 个小节与末尾 languagegui 表格都在文档里', () => {
    const document = projection?.documentMarkdown ?? '';
    expect(document.match(/- \[ \] \*\*AC-/g)).toHaveLength(12);
    expect(document.match(/^## /gm)).toHaveLength(5);
    expect(document).toContain('```languagegui');
    expect(document).toContain('看板可见集合的三处计数口径');
    expect(document.trimEnd().endsWith('```')).toBe(true);
  });

  it('前言保持原位：留在正文里的解释不含文档标题', () => {
    expect(projection?.introMarkdown).toContain('结论先行');
    expect(projection?.introMarkdown).not.toContain('# 《任务看板标题搜索》验收条件');
    expect(markdown.match(/^# 《任务看板标题搜索》验收条件$/gm)).toHaveLength(1);
  });
});

describe('projectDeliveredDocument · 边界', () => {
  it('空串与普通短答不识别', () => {
    expect(projectDeliveredDocument('')).toBeNull();
    expect(projectDeliveredDocument('好的，已完成，需要我再补测试吗？')).toBeNull();
  });

  it('只有标题或只有小标题不识别', () => {
    expect(projectDeliveredDocument(`# 标题\n\n${'说明。'.repeat(300)}`)).toBeNull();
    expect(projectDeliveredDocument(`## 一\n\n${'甲。'.repeat(200)}\n\n## 二\n\n${'乙。'.repeat(200)}`)).toBeNull();
  });

  it('只有一个小节不识别', () => {
    expect(projectDeliveredDocument(`# 《示例》\n\n## 唯一小节\n\n${'正文。'.repeat(400)}`)).toBeNull();
  });

  it('文档本体短于最小长度不识别', () => {
    const markdown = '# 《示例》\n\n## 一\n\n短。\n\n## 二\n\n也短。\n';
    expect(markdown.length).toBeLessThan(800);
    expect(projectDeliveredDocument(markdown)).toBeNull();
  });

  it('前言比文档还长时不识别（避免误判长讨论）', () => {
    const markdown = sectioned('前言段落。'.repeat(400), 100);
    const documentLength = markdown.length - 2000;
    expect(documentLength).toBeGreaterThanOrEqual(800);
    expect(projectDeliveredDocument(markdown)).toBeNull();
  });

  it('紧贴段落文字的标题不当作文档边界', () => {
    expect(projectDeliveredDocument(sectioned('前言正文。'))).toBeNull();
  });

  it('主题分隔线后无空行的标题仍算独立块', () => {
    const markdown = sectioned('前言正文。\n\n---');
    const projection = projectDeliveredDocument(markdown);
    expect(projection).not.toBeNull();
    expect(projection?.introMarkdown).toContain('---');
    expect(projection?.documentMarkdown.startsWith('# 《示例》验收条件')).toBe(true);
  });

  it('前言里的顶层小标题不影响文档起点', () => {
    const markdown = sectioned('## 现状\n\n看板已有本地过滤框。\n');
    const projection = projectDeliveredDocument(markdown);
    expect(projection).not.toBeNull();
    expect(projection?.introMarkdown).toContain('## 现状');
    expect(projection?.documentMarkdown.startsWith('# 《示例》验收条件')).toBe(true);
  });

  it('围栏代码里的 # 不参与识别，也不被切走', () => {
    const markdown = [
      '# 《围栏》验收条件',
      '',
      '## 示例',
      '',
      '```bash',
      '# 这不是标题',
      'echo hi',
      '```',
      '',
      '## 小节二',
      '',
      '正文段落。'.repeat(200),
      '',
    ].join('\n');
    const projection = projectDeliveredDocument(markdown);
    expect(projection).not.toBeNull();
    expect(projection?.introMarkdown).toBe('');
    expect(projection?.documentMarkdown).toBe(markdown);
    expect(projection?.documentMarkdown).toContain('# 这不是标题');
  });

  it('只有围栏里的 # 时整篇不识别', () => {
    const markdown = ['```md', '# 只是示例', '```', '', '正文段落。'.repeat(200)].join('\n');
    expect(projectDeliveredDocument(markdown)).toBeNull();
  });

  it('#hashtag 不是标题', () => {
    const markdown = ['#hashtag 不是标题', '', '## 第一节', '', '正文段落。'.repeat(200)].join('\n');
    expect(projectDeliveredDocument(markdown)).toBeNull();
  });

  it('缩进的一级标题（缩进代码块内容）不当作文档起点，也不被切走', () => {
    const markdown = [
      '# 《缩进》验收条件',
      '',
      '## 第一节',
      '',
      '正文段落。'.repeat(120),
      '',
      '    # 缩进内容不是标题',
      '',
      '## 第二节',
      '',
      '补充说明。'.repeat(120),
      '',
    ].join('\n');
    const projection = projectDeliveredDocument(markdown);
    expect(projection).not.toBeNull();
    expect(projection?.documentMarkdown).toContain('    # 缩进内容不是标题');
  });

  it('CRLF 原文也能识别，且标题不带回车', () => {
    const markdown = sectioned().replace(/\n/g, '\r\n');
    const projection = projectDeliveredDocument(markdown);
    expect(projection).not.toBeNull();
    expect(projection?.title).toBe('《示例》验收条件');
    expect((projection?.introMarkdown ?? '') + (projection?.documentMarkdown ?? '')).toBe(markdown);
    expect(projection?.documentMarkdown.startsWith('# 《示例》验收条件\r\n')).toBe(true);
  });

  it('文档里的第二个一级标题不改切分点，仍留在同一文档内', () => {
    const markdown = `${sectioned('前言。\n')}\n# 附录\n\n${'附注。'.repeat(50)}\n`;
    const projection = projectDeliveredDocument(markdown);
    expect(projection).not.toBeNull();
    expect(projection?.documentMarkdown.match(/^# /gm)).toHaveLength(2);
    expect(projection?.documentMarkdown.startsWith('# 《示例》验收条件')).toBe(true);
    expect(projection?.introMarkdown).toBe('前言。\n\n');
  });
});

describe('documentDownloadName', () => {
  it('中文标题保留可读文件名', () => {
    expect(documentDownloadName('《任务看板标题搜索》验收条件')).toBe('《任务看板标题搜索》验收条件.md');
  });

  it('替换路径与通配等保留字符，并折叠空白', () => {
    expect(documentDownloadName('a/b\\c:d*e?f"g<h>i|j')).toBe('a b c d e f g h i j.md');
    expect(documentDownloadName('多行\n标题')).toBe('多行 标题.md');
    expect(documentDownloadName('控制\u0000字符')).toBe('控制 字符.md');
  });

  it('标题不可用时退回 document.md', () => {
    expect(documentDownloadName('')).toBe('document.md');
    expect(documentDownloadName('   ')).toBe('document.md');
    expect(documentDownloadName('...')).toBe('document.md');
    expect(documentDownloadName('///')).toBe('document.md');
  });

  it('去掉首尾点并截断超长标题', () => {
    expect(documentDownloadName('目录.')).toBe('目录.md');
    expect(documentDownloadName('.hidden')).toBe('hidden.md');
    expect(documentDownloadName('长'.repeat(200))).toBe(`${'长'.repeat(60)}.md`);
  });
});
