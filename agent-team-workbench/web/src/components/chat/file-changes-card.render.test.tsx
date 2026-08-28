import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { RunChanges } from '../../api/types';
import { FileChangesView } from './file-changes-card';

const changes: RunChanges = {
  file_count: 4,
  additions: 45,
  deletions: 3,
  state: 'ready',
  can_revert: true,
  version: 1,
  files: [
    { path: 'a.md', kind: 'added', additions: 40, deletions: 0, write_count: 1, last_turn_index: 1, binary: false },
    { path: 'b.ts', kind: 'modified', additions: 3, deletions: 1, write_count: 1, last_turn_index: 1, binary: false },
    { path: 'c.go', kind: 'modified', additions: 2, deletions: 2, write_count: 2, last_turn_index: 1, binary: false },
    { path: 'd.txt', kind: 'deleted', additions: 0, deletions: 0, write_count: 1, last_turn_index: 1, binary: false },
  ],
};

describe('FileChangesView', () => {
  it('renders a Codex-style per-run summary and limits the initial list to three files', () => {
    const html = renderToStaticMarkup(<FileChangesView data={changes} />);

    expect(html).toContain('已编辑 4 个文件');
    expect(html).toContain('新增 45 行，删除 3 行');
    expect(html).toContain('>+45<');
    expect(html).toContain('>−3<');
    expect(html).toContain('a.md');
    expect(html).toContain('b.ts');
    expect(html).toContain('c.go');
    expect(html).not.toContain('d.txt');
    expect(html).toContain('再显示 1 个文件');
    expect(html).toContain('撤销');
    expect(html).toContain('审核');
  });

  it('marks reverted changes and disables another revert', () => {
    const html = renderToStaticMarkup(
      <FileChangesView data={{ ...changes, state: 'reverted', can_revert: false }} />,
    );

    expect(html).toContain('已撤销');
    expect(html).toContain('title="本轮文件变更已经撤销"');
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>.*撤销/s);
  });

  it('does not rely on color alone for additions and deletions', () => {
    const html = renderToStaticMarkup(<FileChangesView data={changes} />);
    expect(html).toContain('aria-label="a.md，新增 40 行，删除 0 行"');
    expect(html).toContain('+40');
    expect(html).toContain('−0');
  });
});
