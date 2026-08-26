/**
 * Tailwind 透明度修饰刻度门禁：修饰值只认 5 的倍数（v3 默认 opacity 刻度），
 * 其余（如 /92、/62）工具类会被静默跳过——浅色主题下看不出，暗色作用域里
 * 会露 Chrome 表单默认 Field 白底（2026-08-27 审批 composer 白框根因）。
 * 任意值请用方括号形式 bg-black/[0.08]。
 */

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const WEB_ROOT = dirname(dirname(fileURLToPath(import.meta.url)));
const SRC_ROOT = join(WEB_ROOT, 'src');

/** 匹配 tailwind 颜色工具类的纯数字透明度修饰（任意值 [] 形式不匹配）。 */
const ALPHA_MODIFIER =
  /(?:bg|text|border|ring|from|to|via|divide|outline|decoration|shadow|fill|stroke)-[a-z][a-z0-9-]*\/(\d+)(?![\w.\]-])/g;

function listSourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const absolute = join(dir, entry.name);
    if (entry.isDirectory()) return listSourceFiles(absolute);
    return /\.(?:ts|tsx)$/.test(entry.name) ? [absolute] : [];
  });
}

describe('Tailwind 透明度修饰刻度门禁', () => {
  it('颜色工具类透明度修饰必须是 5 的倍数（否则 utility 静默不存在）', () => {
    const violations: string[] = [];
    for (const absolute of listSourceFiles(SRC_ROOT)) {
      const file = relative(WEB_ROOT, absolute);
      readFileSync(absolute, 'utf8').split('\n').forEach((text, index) => {
        ALPHA_MODIFIER.lastIndex = 0;
        let m: RegExpExecArray | null;
        while ((m = ALPHA_MODIFIER.exec(text)) !== null) {
          const n = Number(m[1]);
          if (n <= 100 && n % 5 !== 0) {
            violations.push(`${file}:${index + 1}: /${n} <- ${text.trim().slice(0, 110)}`);
          }
        }
      });
    }
    expect(
      violations,
      `发现非刻度透明度修饰（Tailwind 不生成对应 utility，静默失效）:\n${violations.join('\n')}`,
    ).toEqual([]);
  });
});
