import { describe, expect, it } from 'vitest';
import { normalizeBareLanguageGuiDocuments } from './bare-languagegui';

function documentJson(pretty = false): string {
  const value = {
    version: 'languagegui/v1',
    blocks: [
      {
        type: 'metric',
        title: '需求拆解速览',
        items: [{ label: '需求条目', value: 6, detail: '范围 / 匹配字段', tone: 'neutral' }],
      },
      {
        type: 'table',
        title: '需求点与现状对照',
        columns: [{ key: 'req', label: '需求' }, { key: 'now', label: '当前实现' }],
        rows: [{ req: '只搜标题', now: '匹配 id、WI 短编号、title、description' }],
      },
    ],
  };
  return pretty ? JSON.stringify(value, null, 2) : JSON.stringify(value);
}

function fence(raw: string, closed = true, newline = '\n'): string {
  return closed ? `\`\`\`languagegui${newline}${raw}${newline}\`\`\`` : `\`\`\`languagegui${newline}${raw}`;
}

describe('normalizeBareLanguageGuiDocuments', () => {
  it('wraps a real metric and table document while preserving surrounding paragraphs', () => {
    const raw = documentJson(true);
    const markdown = `前文说明。\n\n${raw}\n\n后文说明。`;
    expect(normalizeBareLanguageGuiDocuments(markdown)).toBe(`前文说明。\n\n${fence(raw)}\n\n后文说明。`);
  });

  it('parses nested braces and escaped strings instead of stopping at the first brace', () => {
    const raw = JSON.stringify({
      version: 'languagegui/v1',
      blocks: [{
        type: 'metric',
        title: '花括号 } { 与引号 "',
        description: '反斜杠 \\、换行\n和字符串里的 { }',
        items: [{ label: '值', value: 'a } { "quoted"', tone: 'neutral' }],
      }],
    }, null, 2);
    const markdown = `说明\n\n${raw}\n\n结尾`;
    const normalized = normalizeBareLanguageGuiDocuments(markdown);
    expect(normalized).toBe(`说明\n\n${fence(raw)}\n\n结尾`);
    expect(normalized).toContain('a } { \\"quoted\\"');
  });

  it('keeps ordinary, versionless, unknown-version and fenced content unchanged', () => {
    const raw = documentJson();
    const ordinary = '{"blocks":[{"type":"metric"}]}';
    const versionless = raw.replace('"version":"languagegui/v1",', '');
    const unknown = raw.replace('languagegui/v1', 'languagegui/v2');
    const markdown = [
      ordinary,
      versionless,
      unknown,
      '`' + raw + '`',
      '    ' + raw,
      '> ' + raw,
      '- ' + raw,
      '```json',
      raw,
      '```',
      '```languagegui',
      raw,
      '```',
    ].join('\n\n');
    expect(normalizeBareLanguageGuiDocuments(markdown)).toBe(markdown);
  });

  it('wraps multiple independent documents in their original order', () => {
    const first = documentJson();
    const second = first.replace('需求拆解速览', '第二份');
    const markdown = `${first}\n\n中间说明\n\n${second}`;
    const normalized = normalizeBareLanguageGuiDocuments(markdown);
    expect(normalized).toBe(`${fence(first)}\n\n中间说明\n\n${fence(second)}`);
  });

  it('keeps a recognized incomplete tail open during streaming', () => {
    const raw = '{"version":"languagegui/v1","blocks":[{"type":"metric","items":[';
    expect(normalizeBareLanguageGuiDocuments(`说明\n\n${raw}`, true)).toBe(`说明\n\n${fence(raw, false)}`);
  });

  it('closes explicitly identified incomplete or invalid protocol output at finalization', () => {
    const incomplete = '{"version":"languagegui/v1","blocks":[{"type":"metric"}';
    const invalid = '{"version":"languagegui/v1","blocks":[{"type":"unknown"}]}';
    expect(normalizeBareLanguageGuiDocuments(incomplete)).toBe(fence(incomplete));
    expect(normalizeBareLanguageGuiDocuments(invalid)).toBe(fence(invalid));
    expect(normalizeBareLanguageGuiDocuments(incomplete, true)).toBe(fence(incomplete, false));
  });

  it('does not consume a following normal paragraph when an explicit tail is incomplete', () => {
    const incomplete = '{"version":"languagegui/v1","blocks":[';
    const markdown = `开头\n\n${incomplete}\n\n后续正常段落`;
    expect(normalizeBareLanguageGuiDocuments(markdown)).toBe(`开头\n\n${fence(incomplete)}\n\n后续正常段落`);
    expect(normalizeBareLanguageGuiDocuments(markdown, true)).toBe(`开头\n\n${fence(incomplete)}\n\n后续正常段落`);
  });

  it('keeps a balanced document with same-paragraph trailing text unchanged', () => {
    const raw = documentJson();
    const markdown = `${raw} 后续仍在同一段`;
    expect(normalizeBareLanguageGuiDocuments(markdown)).toBe(markdown);
  });

  it('leaves oversized documents untouched and is idempotent', () => {
    const oversized = `{"version":"languagegui/v1","blocks":[{"type":"metric","items":[{"label":"x","value":"${'x'.repeat(250_010)}"}]}]}`;
    expect(oversized.length).toBeGreaterThan(250_000);
    expect(normalizeBareLanguageGuiDocuments(oversized)).toBe(oversized);

    const normalized = normalizeBareLanguageGuiDocuments(`前\n\n${documentJson(true)}\n\n后`);
    expect(normalizeBareLanguageGuiDocuments(normalized)).toBe(normalized);
  });

  it('recognizes legal JSON whitespace blank lines without rescanning ordinary candidates', () => {
    const raw = [
      '{',
      '  "version": "languagegui/v1",',
      '',
      '  "blocks": [{',
      '    "type": "metric",',
      '    "items": [{ "label": "x", "value": 1 }]',
      '  }]',
      '}',
    ].join('\n');
    expect(normalizeBareLanguageGuiDocuments(`前\n\n${raw}\n\n后`)).toBe(`前\n\n${fence(raw)}\n\n后`);

    const noise = Array.from({ length: 20_000 }, (_, index) => `{noise-${index}\n\n`).join('');
    const startedAt = performance.now();
    expect(normalizeBareLanguageGuiDocuments(noise)).toBe(noise);
    expect(performance.now() - startedAt).toBeLessThan(1_000);

    const malformed = Array.from({ length: 1_000 }, () => '{"version":"languagegui/v1","blocks":[').join('\n\n');
    const malformedStartedAt = performance.now();
    const malformedResult = normalizeBareLanguageGuiDocuments(malformed);
    expect(performance.now() - malformedStartedAt).toBeLessThan(1_000);
    expect(malformedResult).not.toBe(malformed);
    expect(malformedResult.endsWith('{"version":"languagegui/v1","blocks":[')).toBe(true);
  });

  it('preserves CRLF boundaries for complete and incomplete documents with following text', () => {
    const newline = '\r\n';
    const complete = documentJson(true).replace(/\n/g, newline);
    const completeMarkdown = `前${newline}${newline}${complete}${newline}${newline}后`;
    expect(normalizeBareLanguageGuiDocuments(completeMarkdown)).toBe(`前${newline}${newline}${fence(complete, true, newline)}${newline}${newline}后`);

    const incomplete = '{"version":"languagegui/v1","blocks":[';
    const incompleteMarkdown = `前${newline}${newline}${incomplete}${newline}${newline}后`;
    expect(normalizeBareLanguageGuiDocuments(incompleteMarkdown)).toBe(`前${newline}${newline}${fence(incomplete, true, newline)}${newline}${newline}后`);
    expect(normalizeBareLanguageGuiDocuments(incompleteMarkdown, true)).toBe(`前${newline}${newline}${fence(incomplete, true, newline)}${newline}${newline}后`);
  });
});
