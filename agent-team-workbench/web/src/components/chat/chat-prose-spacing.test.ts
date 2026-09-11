/**
 * 聊天正文排版节奏门禁：`.chat-prose > * + *`（特异性 (0,1,0)）统一提供块级
 * 子元素的垂直间距。2026-09 实测回归：`.chat-prose ul/ol/blockquote` 等高特
 * 异性的 margin 归零规则压过节奏规则，段落→列表/引用 的间距被吃成 3px 甚至
 * 0px；且 `:where()` 只归零特异性，同为 (0,1,0) 时后声明胜出，归零规则若排在
 * 节奏规则之后同样压平间距。
 *
 * 修复约定（与 `.chat-prose :where(p)` 同池）：
 * 1. margin 归零必须写进 :where()，不得裸挂在元素选择器上；
 * 2. 归零规则必须声明在 `.chat-prose > * + *` 之前。
 * 本门禁扫描 src/index.css 文本钉死这两条，并钉住节奏规则本体不被删除。
 */

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/** 本文件位于 web/src/components/chat/ 下，向上两级即 src/ 根。 */
const THIS_DIR = dirname(fileURLToPath(import.meta.url));
const SRC_ROOT = dirname(dirname(THIS_DIR));
const CSS_PATH = join(SRC_ROOT, 'index.css');

/** 直接读源文件做文本断言，与 design-tokens.test.ts 同一模式；先去注释再解析。 */
const CSS_TEXT = readFileSync(CSS_PATH, 'utf8').replace(/\/\*[\s\S]*?\*\//g, ' ');

interface CssRule {
  selector: string;
  body: string;
  /** 规则起点在去注释文本中的偏移，用于断言声明顺序。 */
  offset: number;
}

/** 轻量规则块扫描：本项目规则不嵌套，@media/@keyframes 壳以 @ 开头跳过，内部规则独立成块。 */
function parseRules(css: string): CssRule[] {
  const rules: CssRule[] = [];
  for (const match of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = match[1].trim();
    if (selector.length === 0 || selector.startsWith('@')) continue;
    rules.push({ selector, body: match[2], offset: match.index ?? 0 });
  }
  return rules;
}

const rules = parseRules(CSS_TEXT);

/** 可作为 .chat-prose 直接子级出现的块级元素，其垂直间距归节奏规则统一管。 */
const PROSE_ROOT_ELEMENTS = [
  'p',
  'ul',
  'ol',
  'blockquote',
  'pre',
  'dl',
  'table',
  'details',
  'figure',
  'img',
  'hr',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
] as const;

/** .chat-prose 作用域下（后代/子代）出现上述块元素的选择器形态。 */
const PROSE_ELEMENT_PATTERN = new RegExp(
  `\\.chat-prose\\s*(?:>|\\+|~)?\\s*(?:${PROSE_ROOT_ELEMENTS.join('|')})\\b`,
);

/** 展开 :where( 为裸内容，使元素写在 :where() 里也能被识别。 */
function flattenWhere(selector: string): string {
  return selector.replace(/:where\(/g, ' ').replace(/\)/g, ' ');
}

/** 剥掉整个 :where(...) 组，用于判断元素是否裸露（:where 之外）出现。 */
function stripWhere(selector: string): string {
  return selector.replace(/:where\([^)]*\)/g, ' ');
}

/** margin 归零声明：所有分量只能是 0（px 可选）或 auto，且至少一个分量为 0。 */
function zeroingMarginDeclarations(body: string): string[] {
  const zeroing: string[] = [];
  for (const declaration of body.split(';')) {
    const match = declaration.trim().match(/^(margin(?:-top|-block)?)\s*:\s*([^]+)$/);
    if (match === null) continue;
    const values = match[2].trim().split(/\s+/);
    const isZeroOrAuto = (value: string): boolean => /^(?:0|0px|auto)$/.test(value);
    if (values.some((value) => /^(?:0|0px)$/.test(value)) && values.every(isZeroOrAuto)) {
      zeroing.push(`${match[1]}: ${match[2].trim()}`);
    }
  }
  return zeroing;
}

const rhythmRule = rules.find(
  (rule) => /\.chat-prose\s*>\s*\*\s*\+\s*\*/.test(rule.selector) && /margin-top:\s*0\.85em/.test(rule.body),
);

describe('chat-prose 排版节奏门禁', () => {
  it('节奏规则 .chat-prose > * + * 存在且给出 0.85em 间距', () => {
    expect(rhythmRule, '节奏规则本体不得被删除或改值').toBeDefined();
  });

  it('margin 归零不得裸挂在 .chat-prose 块级元素的高特异性选择器上', () => {
    const violations: string[] = [];
    for (const rule of rules) {
      const zeroing = zeroingMarginDeclarations(rule.body);
      if (zeroing.length === 0) continue;
      if (!PROSE_ELEMENT_PATTERN.test(flattenWhere(rule.selector))) continue;
      if (PROSE_ELEMENT_PATTERN.test(stripWhere(rule.selector))) {
        violations.push(`${rule.selector} { ${zeroing.join('; ')} }`);
      }
    }
    expect(
      violations,
      `高特异性 margin 归零会压过 .chat-prose > * + *（应为 ~12.75px 的段落→列表/引用间距被吃掉）。` +
        `请把 margin 归零改写进 :where() 并置于节奏规则之前:\n${violations.join('\n')}`,
    ).toEqual([]);
  });

  it('margin 归零规则必须声明在 > * + * 之前（同特异性时后声明胜出）', () => {
    const violations: string[] = [];
    for (const rule of rules) {
      if (zeroingMarginDeclarations(rule.body).length === 0) continue;
      if (!PROSE_ELEMENT_PATTERN.test(flattenWhere(rule.selector))) continue;
      if (rhythmRule === undefined || rule.offset > rhythmRule.offset) {
        violations.push(`${rule.selector}: 归零声明必须位于 .chat-prose > * + * 之前`);
      }
    }
    expect(violations, `同为 (0,1,0) 特异性时后声明胜出，放在节奏规则之后会重新压平间距:\n${violations.join('\n')}`).toEqual(
      [],
    );
  });
});
