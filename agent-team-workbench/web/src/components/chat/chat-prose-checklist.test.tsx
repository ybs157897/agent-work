/**
 * 任务清单（GFM task list）排版门禁。
 *
 * 2026-09 实测回归：`.chat-prose` 只写了紧列表形态（`li > input`）的样式，
 * GFM 的松列表（条目含空行或多段，例如「- [ ] AC-01…」后跟缩进的「判定：…」
 * 段）会把条目内容包进 `<p>`，于是 `li:has(> input)` 落空：项目符号重新出现
 * （项目符号 + 复选框双 marker），条目内的多段也失去间距。
 *
 * 本门禁同时钉住两件事：
 * 1. 渲染形态——松列表确实产出 `li > p > input`，`#` 断言的不是想象的结构；
 * 2. 样式覆盖——紧/松两种形态都吃到去符号与复选框缩进，且 `li > p + p` 有间距。
 */

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { MarkdownBody } from './markdown-body';

/** 本文件位于 web/src/components/chat/ 下，向上两级即 src/ 根。 */
const THIS_DIR = dirname(fileURLToPath(import.meta.url));
const SRC_ROOT = dirname(dirname(THIS_DIR));
const CSS_TEXT = readFileSync(join(SRC_ROOT, 'index.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, ' ');

interface CssRule {
  /** 逗号分隔的选择器列表，空白已折叠，便于逐条 endsWith 断言。 */
  selectors: string[];
  body: string;
}

function parseRules(css: string): CssRule[] {
  const rules: CssRule[] = [];
  for (const match of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = match[1].trim();
    if (selector.length === 0 || selector.startsWith('@')) continue;
    rules.push({
      selectors: selector.split(',').map((item) => item.trim().replace(/\s+/g, ' ')),
      body: match[2],
    });
  }
  return rules;
}

const rules = parseRules(CSS_TEXT);

/** 查找“某个选择器分支带来了某条声明”的规则；找不到即未覆盖。 */
function coveringRule(selector: string, declaration: RegExp): string | undefined {
  return rules
    .filter((rule) => rule.selectors.some((item) => item.endsWith(selector)) && declaration.test(rule.body))
    .map((rule) => rule.selectors.join(', '))
    .join(' | ') || undefined;
}

describe('chat-prose 任务清单门禁', () => {
  it('松列表渲染形态为 li > p > input（不是 li > input）', () => {
    const loose = renderToStaticMarkup(
      <MarkdownBody text={'- [ ] 第一项。\n\n  判定：继续。\n\n- [x] 第二项。\n'} />,
    );
    expect(loose).toMatch(/<li[^>]*>\s*<p><input[^>]*type="checkbox"/);
    expect(loose).toContain('<p>判定：继续。</p>');

    const tight = renderToStaticMarkup(<MarkdownBody text={'- [ ] 第一项。\n- [x] 第二项。\n'} />);
    expect(tight).toMatch(/<li[^>]*><input[^>]*type="checkbox"/);
    expect(tight).not.toContain('<p><input');
  });

  it('紧列表形态继续去项目符号并缩进复选框', () => {
    expect(coveringRule("li:has(> input[type='checkbox'])", /list-style:\s*none/)).toBeDefined();
    expect(coveringRule("li > input[type='checkbox']", /margin-right:\s*0\.55em/)).toBeDefined();
  });

  it('松列表形态（li > p > input）同样去项目符号并缩进复选框', () => {
    expect(
      coveringRule("li:has(> p > input[type='checkbox'])", /list-style:\s*none/),
      '松列表漏掉 list-style: none 会出现项目符号 + 复选框双 marker',
    ).toBeDefined();
    expect(
      coveringRule("li > p > input[type='checkbox']", /margin-right:\s*0\.55em/),
      '松列表漏掉复选框 margin-right 会让复选框与文字贴住',
    ).toBeDefined();
  });

  it('松列表条目内的多段保留间距', () => {
    expect(
      coveringRule('li > p + p', /margin-top:\s*0\.6em/),
      'li 内部不像 .chat-prose 直接子级那样吃到节奏规则，多段会贴在一起',
    ).toBeDefined();
  });
});
