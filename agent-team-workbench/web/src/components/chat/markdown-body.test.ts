// 只钉可离线验证的纯函数（hasHanText / tableToTsv）。
// ErrorBoundary 与 TableCard 是组件：本仓库 vitest 跑 node 环境（vite.config.ts
// test.environment: 'node'）无 DOM/渲染器，不在此测组件行为。
import { describe, expect, it } from 'vitest';
import { hasHanText, shouldAnimateMarkdown } from './markdown-body';
import { textGenerateTokens } from '../aceternity/text-generate-effect';
import { tableToTsv } from './table-card';

describe('hasHanText（\\p{Script=Han} 检测，CJK 段距规则的判定面）', () => {
  it('含中日韩汉字 → true', () => {
    expect(hasHanText('中文段落')).toBe(true);
    expect(hasHanText('日本語のテキスト')).toBe(true);
    expect(hasHanText('mixed 漢字 text')).toBe(true);
  });

  it('纯英文/数字/标点/空白 → false', () => {
    expect(hasHanText('')).toBe(false);
    expect(hasHanText('plain english')).toBe(false);
    expect(hasHanText('123 + 456')).toBe(false);
    expect(hasHanText('!@#$%')).toBe(false);
    expect(hasHanText('   ')).toBe(false);
  });

  it('其他文字系统不误伤：拉丁扩展、假名、谚文均非 Han 脚本', () => {
    expect(hasHanText('café über')).toBe(false);
    expect(hasHanText('スパシーバ')).toBe(false);
    expect(hasHanText('한국어')).toBe(false);
  });
});

describe('Markdown motion contract', () => {
  it('只在完成态启用正文显现，流式状态保持静态以避免 token 抖动', () => {
    expect(shouldAnimateMarkdown(true)).toBe(false);
    expect(shouldAnimateMarkdown(false)).toBe(true);
  });

  it('中文标题按汉字分段，拉丁文本保持词组与空格', () => {
    expect(textGenerateTokens('维护性风险')).toEqual(['维', '护', '性', '风', '险']);
    expect(textGenerateTokens('Risk review')).toEqual(['Risk', ' ', 'review']);
  });
});

describe('tableToTsv（表格复制内容的拼装）', () => {
  it('基本转换：\\t 拼列、\\n 拼行', () => {
    expect(
      tableToTsv([
        ['a', 'b'],
        ['c', 'd'],
      ]),
    ).toBe('a\tb\nc\td');
  });

  it('空单元格保留占位（连续分隔符不塌缩）', () => {
    expect(
      tableToTsv([
        ['a', ''],
        ['', ''],
      ]),
    ).toBe('a\t\n\t');
  });

  it('单行单格与空表', () => {
    expect(tableToTsv([['only']])).toBe('only');
    expect(tableToTsv([])).toBe('');
  });
});
