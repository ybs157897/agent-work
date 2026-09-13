import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ApiError } from '../../../api/client';
import { parseContentBlockDocument, previewPathOf, repoRelativePath } from '../../../utils/content-blocks';
import { FileBlock } from './file-block';
import { runFileErrorMessage } from './run-file-drawer';

function fileDocument(files: Array<Record<string, unknown>>) {
  const document = parseContentBlockDocument({ version: 'languagegui/v1', blocks: [{ type: 'file', title: '需求文档', files }] });
  if (!document) throw new Error('file block 未解析');
  return document;
}

describe('file block 站内打开', () => {
  it('带仓库相对路径与 Run 时渲染打开动作', () => {
    const document = fileDocument([{ name: 'notes/plan.md', mime: 'text/markdown', size: '10.0 KB', path: 'notes/plan.md' }]);
    const html = renderToStaticMarkup(<FileBlock block={document.blocks[0] as never} runId="run_1" />);
    expect(html).toContain('aria-label="打开文件：notes/plan.md"');
    expect(html).toContain('<button');
  });

  it('没有 Run 时不渲染打开动作（详情页之外的复用场景）', () => {
    const document = fileDocument([{ name: 'notes/plan.md', path: 'notes/plan.md' }]);
    const html = renderToStaticMarkup(<FileBlock block={document.blocks[0] as never} />);
    expect(html).not.toContain('打开文件：');
  });

  it('历史消息用含路径分隔符的展示名兜底，纯文件名不兜底', () => {
    const legacy = fileDocument([{ name: 'notes/proposed/feature/x.md', status: 'draft' }]);
    expect(renderToStaticMarkup(<FileBlock block={legacy.blocks[0] as never} runId="run_1" />)).toContain('打开文件：notes/proposed/feature/x.md');
    const bare = fileDocument([{ name: '需求.md' }]);
    expect(renderToStaticMarkup(<FileBlock block={bare.blocks[0] as never} runId="run_1" />)).not.toContain('打开文件：');
  });

  it('绝对路径与回退段不产生打开动作', () => {
    const document = fileDocument([
      { name: '/etc/passwd' },
      { name: '../../secret.md' },
      { name: 'ok.md', path: '/Users/someone/secret.md' },
      { name: 'ok2.md', path: '.git/config' },
    ]);
    const html = renderToStaticMarkup(<FileBlock block={document.blocks[0] as never} runId="run_1" />);
    expect(html).not.toContain('打开文件：');
  });

  it('安全外链仍照常渲染', () => {
    const document = fileDocument([{ name: 'report.pdf', url: 'https://example.com/report.pdf' }]);
    expect(renderToStaticMarkup(<FileBlock block={document.blocks[0] as never} runId="run_1" />)).toContain('href="https://example.com/report.pdf"');
  });
});

describe('文件预览路径解析', () => {
  it('收敛分隔符并拒绝越界路径', () => {
    expect(repoRelativePath('notes//plan.md')).toBe('notes/plan.md');
    expect(repoRelativePath('notes\\plan.md')).toBe('notes/plan.md');
    expect(repoRelativePath('/abs/plan.md')).toBeUndefined();
    expect(repoRelativePath('a/../../b.md')).toBeUndefined();
    expect(repoRelativePath('.git/config')).toBeUndefined();
    expect(repoRelativePath('')).toBeUndefined();
  });

  it('显式 path 优先于展示名兜底', () => {
    expect(previewPathOf({ name: '需求.md', path: 'notes/真实路径.md' })).toBe('notes/真实路径.md');
    expect(previewPathOf({ name: '需求.md' })).toBeUndefined();
  });
});

describe('预览错误文案', () => {
  it('按 problem code 给出可读说明', () => {
    const problem = (code: string) => new ApiError({ type: 'about:blank', title: 'x', status: 400, code });
    expect(runFileErrorMessage(problem('run_file_not_found'))).toContain('找不到该文件');
    expect(runFileErrorMessage(problem('run_file_unsupported'))).toContain('不预览');
    expect(runFileErrorMessage(problem('run_file_too_large'))).toContain('1 MB');
    expect(runFileErrorMessage(problem('run_file_unavailable'))).toContain('宿主');
    expect(runFileErrorMessage(new Error('boom'))).toBe('boom');
  });
});
