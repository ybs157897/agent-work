/**
 * 设计 token 纪律门禁（DESIGN.md「Don'ts」第 1 条的防回归断言）：扫描 src 下
 * 全部 TS/TSX 源码，拦截内联色值——十六进制字面量与 rgb/rgba/hsl/hsla 函数
 * 调用。颜色只允许经语义 token（Tailwind 语义名或 hsl(var(--color-*))）引用，
 * 事实源见 web/DESIGN.md。
 */

import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/** 扫描与豁免/报告路径一律以 web/ 为根（与 DESIGN.md 的引用方式一致）。 */
const WEB_ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const SRC_ROOT = join(WEB_ROOT, 'src');

/** 十六进制色字面量（3/4/6/8 位），任何文件一律禁止。 */
const HEX_COLOR = /#(?:[0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})\b/g;

/**
 * 函数式色值字面量：rgb/rgba/hsl/hsla 调用。hsl(var(--color-*)) 是 DESIGN.md
 * 认可的语义 token 引用形式，不算违规；其余调用一律拦截。
 */
const FUNCTIONAL_COLOR = /\b(?:rgba?|hsla?)\((?!var\(--color-)/g;

/**
 * 非语义类名：text-white 与 Tailwind 默认调色板色类。颜色必须走语义 token
 *（含 identity-* 身份色阶）；类名违规拦不住字面量正则，单独钉一道。
 */
const NON_SEMANTIC_CLASS =
  /\b(?:text-white|(?:bg|text|border|ring|from|to)-(?:sky|cyan|teal|emerald|blue|amber|slate|rose|red|green|yellow|orange|purple|indigo|violet|pink)-\d)/g;

/** 本门禁自身：检测正则与注释示例会命中规则，排除扫描。 */
const SELF_FILE = 'src/design-tokens.test.ts';

/**
 * 唯一合法豁免：终端 ANSI 色协议映射层。该文件位于协议键侧——输入是终端发
 * 来的 'r, g, b' 三元组字符串，输出除 256 色/truecolor 直通外全部映射到
 * hsl(var(--color-*)) token 引用；它是协议适配器，不是设计取值。
 */
const EXEMPT_FILES = ['src/components/chat/blocks/ansi.ts'];

/**
 * 测试文件免查函数式色值：它们需要断言被豁免协议面的直通输出（如
 * blocks.test.ts 断言 256 色落到字面 rgb 三元组）；十六进制仍全局禁止。
 */
const TEST_FILE = /\.test\.tsx?$/;

interface Violation {
  file: string;
  line: number;
  snippet: string;
}

function listSourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const absolute = join(dir, entry.name);
    if (entry.isDirectory()) return listSourceFiles(absolute);
    return /\.(?:ts|tsx)$/.test(entry.name) ? [absolute] : [];
  });
}

function collectViolations(): Violation[] {
  const violations: Violation[] = [];
  for (const absolute of listSourceFiles(SRC_ROOT)) {
    const file = relative(WEB_ROOT, absolute);
    if (file === SELF_FILE || EXEMPT_FILES.includes(file)) continue;
    const skipFunctional = TEST_FILE.test(file);
    readFileSync(absolute, 'utf8').split('\n').forEach((text, index) => {
      const hits = [
        ...(skipFunctional ? [] : text.match(FUNCTIONAL_COLOR) ?? []),
        ...(text.match(HEX_COLOR) ?? []),
        ...(text.match(NON_SEMANTIC_CLASS) ?? []),
      ];
      if (hits.length > 0) {
        violations.push({
          file,
          line: index + 1,
          snippet: `${hits.join(', ')} <- ${text.trim().slice(0, 100)}`,
        });
      }
    });
  }
  return violations;
}

describe('设计 token 纪律门禁', () => {
  it('豁免清单只包含 ansi.ts 一项，防止静默扩容', () => {
    expect(EXEMPT_FILES).toEqual(['src/components/chat/blocks/ansi.ts']);
  });

  it('豁免清单里的每个文件都必须真实存在', () => {
    for (const file of EXEMPT_FILES) {
      expect(existsSync(join(WEB_ROOT, file)), `豁免文件已不存在，请同步删除豁免条目: ${file}`).toBe(true);
    }
  });

  it('src 源码不出现内联色值与非语义类名（text-white/默认调色板）', () => {
    const violations = collectViolations();
    expect(
      violations,
      `发现内联色值或非语义类名，请改引用 DESIGN.md 的语义 token（头像用 identity-*）:\n${violations
        .map((v) => `${v.file}:${v.line}: ${v.snippet}`)
        .join('\n')}`,
    ).toEqual([]);
  });
});
