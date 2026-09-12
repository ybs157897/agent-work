/**
 * 聊天正文 details/summary 样式边界门禁：markdown 渲染管线产不出
 * <details>/<summary>（无 rehype-raw），`.chat-prose` 下的 details 全部来自
 * 内容块——chart-block 的 `.chat-content-chart-data` 与 review-summary-block
 * 的 `.chat-review-finding`，二者各自带有完整样式。2026-09 实测回归：通用
 * `.chat-prose details` / `.chat-prose details > summary`（特异性 (0,1,1) 与
 * (0,1,2)）压过内容块自有类（(0,1,0) 与 (0,1,1)），评审项 3px 严重度色条被
 * 覆盖成 1px 灰边，图表数据表 summary 的 padding 与字号被改写。
 *
 * 约定：`.chat-prose` 作用域内不得出现 details/summary 通用元素规则；
 * details 样式由内容块各自的 `chat-content-*` 与 `chat-review-*` 类负责。
 */

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/** 本文件位于 web/src/components/chat/ 下，向上两级即 src/ 根。 */
const THIS_DIR = dirname(fileURLToPath(import.meta.url));
const SRC_ROOT = dirname(dirname(THIS_DIR));
const CSS_PATH = join(SRC_ROOT, 'index.css');

/** 直接读源文件做文本断言，与 chat-prose-spacing.test.ts 同一模式；先去注释再匹配。 */
const CSS_TEXT = readFileSync(CSS_PATH, 'utf8').replace(/\/\*[\s\S]*?\*\//g, ' ');

describe('chat-prose details 样式边界门禁', () => {
  it('.chat-prose 作用域内不得出现 details/summary 通用规则', () => {
    const violations = CSS_TEXT.match(/[^{}\n]*\.chat-prose[^{}\n]*\b(?:details|summary)\b[^{};]*/g) ?? [];
    expect(
      violations,
      `通用 details/summary 规则会以更高特异性覆盖内容块自有样式` +
        `（评审项 3px 严重度色条、图表数据表 summary 的 padding/字号）。` +
        `details 样式归内容块各自的 chat-content-*/chat-review-* 类:\n${violations.join('\n')}`,
    ).toEqual([]);
  });

  it('评审项与图表数据表的自有样式仍在（本门禁保护的契约）', () => {
    expect(CSS_TEXT).toMatch(/\.chat-review-finding\s*\{[^}]*border-left:\s*3px[^}]*--chat-review-tone/);
    expect(CSS_TEXT).toMatch(/\.chat-content-chart-data\s*\{[^}]*mt-tight/);
  });
});
