import { describe, expect, it } from 'vitest';
import { artifactBasename, classifyMime, formatBytes } from './artifact-visuals';

describe('classifyMime', () => {
  it('表格类（csv/tsv）归 data', () => {
    expect(classifyMime('text/csv')).toBe('data');
    expect(classifyMime('application/csv')).toBe('data');
    expect(classifyMime('text/tab-separated-values')).toBe('data');
  });

  it('image/* 归 image', () => {
    expect(classifyMime('image/png')).toBe('image');
    expect(classifyMime('IMAGE/JPEG')).toBe('image');
  });

  it('文本与结构化文档归 text', () => {
    expect(classifyMime('text/plain')).toBe('text');
    expect(classifyMime('text/markdown')).toBe('text');
    expect(classifyMime('application/json')).toBe('text');
    expect(classifyMime('application/xml')).toBe('text');
    expect(classifyMime('application/x-yaml')).toBe('text');
  });

  it('其余与空串归 binary', () => {
    expect(classifyMime('application/octet-stream')).toBe('binary');
    expect(classifyMime('application/pdf')).toBe('binary');
    expect(classifyMime('')).toBe('binary');
  });
});

describe('artifactBasename', () => {
  it('取最后一段路径', () => {
    expect(artifactBasename('reports/2026/risk.md')).toBe('risk.md');
    expect(artifactBasename('/abs/path/table.csv')).toBe('table.csv');
  });

  it('单文件名原样返回', () => {
    expect(artifactBasename('notes.txt')).toBe('notes.txt');
  });

  it('尾斜杠与纯斜杠回落', () => {
    expect(artifactBasename('a/b/')).toBe('b');
    expect(artifactBasename('/')).toBe('/');
    expect(artifactBasename('')).toBe('');
  });
});

describe('formatBytes', () => {
  it('字节档直接取整', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1023)).toBe('1023 B');
  });

  it('KB/MB 一位小数且去掉 .0', () => {
    expect(formatBytes(1024)).toBe('1 KB');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(1048576)).toBe('1 MB');
    expect(formatBytes(1048576 * 2.5)).toBe('2.5 MB');
  });

  it('GB 封顶不再进位', () => {
    expect(formatBytes(1024 ** 3 * 3)).toBe('3 GB');
    expect(formatBytes(1024 ** 4 * 2)).toBe('2048 GB');
  });

  it('负数与 NaN 归 0 B', () => {
    expect(formatBytes(-1)).toBe('0 B');
    expect(formatBytes(Number.NaN)).toBe('0 B');
  });
});
