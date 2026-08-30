import { describe, expect, it } from 'vitest';
import {
  CONTENT_BLOCK_VERSION,
  isSafeContentUrl,
  parseContentBlockDocument,
  parseLanguageGuiFenceDocument,
  stripLanguageGuiFences,
} from './content-blocks';

const envelope = (blocks: unknown[]) => ({ version: CONTENT_BLOCK_VERSION, blocks });

describe('parseContentBlockDocument', () => {
  it('normalizes all v1 block types and drops unknown DOM-like fields', () => {
    const document = parseContentBlockDocument(envelope([
      {
        id: 'metrics-1',
        type: 'metric',
        title: '关键指标',
        items: [{ label: '收入', value: 2480.58, delta: '+12%', tone: 'positive', onClick: 'alert(1)' }],
      },
      {
        type: 'table',
        title: 'Top markets',
        columns: [
          { key: 'country', label: '国家' },
          { key: 'gdp', label: 'GDP', align: 'right' },
        ],
        rows: [{ country: 'US', gdp: 25.4, ignored: '<script />' }],
      },
      {
        type: 'chart',
        chart: 'line',
        labels: ['Mon', 'Tue'],
        series: [{ name: 'Revenue', values: [10, 14] }],
        unit: 'USD',
      },
      {
        type: 'file',
        files: [{ name: 'report.csv', size: 2400, mime: 'text/csv', status: 'ready', url: '/files/report.csv' }],
      },
      {
        type: 'event',
        title: 'Release review',
        start: '2026-08-28T09:00:00+08:00',
        end: '2026-08-28T10:00:00+08:00',
        location: 'Shanghai',
        timezone: 'Asia/Shanghai',
        url: 'https://example.com/event',
      },
      {
        type: 'image',
        images: [{ src: '/images/chart.png', alt: '趋势图', caption: '演示图片' }],
      },
      {
        type: 'audio',
        tracks: [{ src: 'https://example.com/briefing.mp3', title: '语音简报', duration: '02:18' }],
      },
      {
        type: 'map',
        location: '上海市',
        latitude: 31.2304,
        longitude: 121.4737,
        image_url: '/images/map.png',
        url: 'https://www.openstreetmap.org/',
      },
      {
        type: 'search',
        query: 'LanguageGUI',
        results: [{ title: 'LanguageGUI 官网', url: 'https://languagegui.com/', snippet: '<script>text only</script>', source: 'languagegui.com' }],
      },
      { type: 'rating', question: '有帮助吗？', low_label: '没有', high_label: '非常有帮助' },
      {
        type: 'review-summary',
        title: 'Release review',
        verdict: 'changes_requested',
        summary: '发现一个需要修复的问题。\u0000',
        stats: { files: 2, findings: 99, passed: 99 },
        findings: [{ severity: 'high', title: '状态未收口', detail: '<script>文本</script>', file: 'runner.go', line: 214, evidence: 'go test ./...', suggestion: '补充失败路径', url: 'javascript:alert(1)' }],
        checks: [{ label: 'TypeScript', status: 'passed', command: 'pnpm tsc --noEmit' }],
        next_steps: [{ label: '修复并重新验证' }],
        onClick: 'alert(1)',
      },
    ]));

    expect(document?.blocks.map((block) => block.type)).toEqual(['metric', 'table', 'chart', 'file', 'event', 'image', 'audio', 'map', 'search', 'rating', 'review-summary']);
    expect(document?.blocks[0]).toEqual({
      id: 'metrics-1',
      type: 'metric',
      title: '关键指标',
      items: [{ label: '收入', value: '2480.58', delta: '+12%', tone: 'positive' }],
    });
    expect(document?.blocks[1].type === 'table' && document.blocks[1].rows[0]).toEqual({ country: 'US', gdp: 25.4 });
    expect(document?.blocks[7]).toMatchObject({ type: 'map', location: '上海市', latitude: 31.2304, longitude: 121.4737 });
    expect(document?.blocks[8].type === 'search' && document.blocks[8].results[0].snippet).toBe('<script>text only</script>');
    expect(document?.blocks[9]).toMatchObject({ type: 'rating', question: '有帮助吗？', lowLabel: '没有', highLabel: '非常有帮助' });
    expect(document?.blocks[10]).toMatchObject({
      type: 'review-summary',
      verdict: 'changes_requested',
      summary: '发现一个需要修复的问题。',
      stats: { files: 2, findings: 1, passed: 1 },
      findings: [{ severity: 'high', detail: '<script>文本</script>', file: 'runner.go', line: 214, suggestion: '补充失败路径' }],
      nextSteps: [{ label: '修复并重新验证' }],
    });
    const reviewBlock = document?.blocks[10];
    expect(reviewBlock?.type === 'review-summary' ? reviewBlock.findings[0]?.url : undefined).toBeUndefined();
  });

  it('rejects incomplete review summaries and caps every review collection', () => {
    expect(parseContentBlockDocument(envelope([{ type: 'review-summary', verdict: 'passed', summary: 'ok', findings: [], checks: [] }]))).toBeNull();
    const parsed = parseContentBlockDocument(envelope([{
      type: 'review-summary', verdict: 'passed', summary: 'ok',
      findings: Array.from({ length: 40 }, (_, i) => ({ severity: 'info', title: `f${i}`, detail: 'detail' })),
      checks: Array.from({ length: 30 }, (_, i) => ({ label: `c${i}`, status: 'skipped' })),
      next_steps: Array.from({ length: 30 }, (_, i) => ({ label: `n${i}` })),
    }]));
    const block = parsed?.blocks[0];
    expect(block?.type === 'review-summary' && block.findings).toHaveLength(30);
    expect(block?.type === 'review-summary' && block.checks).toHaveLength(20);
    expect(block?.type === 'review-summary' && block.nextSteps).toHaveLength(12);
  });

  it('accepts JSON input, keeps valid siblings and rejects unsupported documents', () => {
    const parsed = parseContentBlockDocument(JSON.stringify(envelope([
      { type: 'unknown', text: 'drop me' },
      { type: 'metric', items: [{ label: 'Valid', value: '42' }] },
      { type: 'chart', chart: 'bar', labels: ['x'], series: [{ name: 'bad', values: [Number.NaN] }] },
    ])));
    expect(parsed?.blocks).toHaveLength(1);
    expect(parseContentBlockDocument('{bad')).toBeNull();
    expect(parseContentBlockDocument({ version: 'languagegui/v2', blocks: [] })).toBeNull();
    expect(parseContentBlockDocument(envelope([]))).toBeNull();
    expect(parseContentBlockDocument(null)).toBeNull();
  });

  it('caps block, metric, table, chart and file collections', () => {
    const metrics = Array.from({ length: 20 }, (_, index) => ({ label: `M${index}`, value: index }));
    const rows = Array.from({ length: 130 }, (_, index) => ({ value: index }));
    const files = Array.from({ length: 30 }, (_, index) => ({ name: `${index}.txt` }));
    const blocks = [
      { type: 'metric', items: metrics },
      { type: 'table', columns: [{ key: 'value', label: 'Value' }], rows },
      { type: 'chart', chart: 'bar', labels: Array.from({ length: 70 }, (_, i) => String(i)), series: [{ name: 'S', values: Array.from({ length: 70 }, (_, i) => i) }] },
      { type: 'file', files },
      ...Array.from({ length: 20 }, () => ({ type: 'metric', items: [{ label: 'x', value: 1 }] })),
    ];
    const parsed = parseContentBlockDocument(envelope(blocks));
    expect(parsed?.blocks).toHaveLength(12);
    expect(parsed?.blocks[0].type === 'metric' && parsed.blocks[0].items).toHaveLength(8);
    expect(parsed?.blocks[1].type === 'table' && parsed.blocks[1].rows).toHaveLength(100);
    expect(parsed?.blocks[2].type === 'chart' && parsed.blocks[2].labels).toHaveLength(64);
    expect(parsed?.blocks[3].type === 'file' && parsed.blocks[3].files).toHaveLength(20);
  });

  it('never forwards unsafe URLs, class/style or event-handler fields', () => {
    const parsed = parseContentBlockDocument(envelope([
      {
        type: 'file',
        className: 'fixed inset-0',
        style: { color: 'red' },
        files: [
          { name: 'unsafe.html', url: ' JAVASCRIPT:alert(1)', onClick: 'alert(1)' },
          { name: 'safe.txt', url: 'https://example.com/safe.txt' },
        ],
        source: { label: 'unsafe source', url: 'data:text/html,<script>alert(1)</script>' },
      },
    ]));
    expect(parsed?.blocks[0]).toEqual({
      type: 'file',
      source: { label: 'unsafe source' },
      files: [
        { name: 'unsafe.html' },
        { name: 'safe.txt', url: 'https://example.com/safe.txt' },
      ],
    });
  });
});

describe('content block URL and fence safety', () => {
  it('infers v1 only when an explicit languagegui fence omits its redundant version', () => {
    const source = JSON.stringify({
      blocks: [{
        type: 'table', title: '十道题汇总',
        columns: [{ key: 'answer', label: '答案' }],
        rows: [{ answer: 'x = 3' }],
      }],
    });
    expect(parseContentBlockDocument(source)).toBeNull();
    expect(parseLanguageGuiFenceDocument(source)).toMatchObject({
      version: CONTENT_BLOCK_VERSION,
      blocks: [{ type: 'table', title: '十道题汇总' }],
    });
    expect(parseLanguageGuiFenceDocument({ version: 'languagegui/v2', blocks: [] })).toBeNull();
    expect(parseLanguageGuiFenceDocument({ blocks: [{ type: 'unknown' }] })).toBeNull();
    expect(parseLanguageGuiFenceDocument({ rows: [] })).toBeNull();
  });

  it('uses a narrow URL allowlist', () => {
    expect(isSafeContentUrl('https://example.com')).toBe(true);
    expect(isSafeContentUrl('http://localhost/file')).toBe(true);
    expect(isSafeContentUrl('/api/files/1')).toBe(true);
    expect(isSafeContentUrl('./file.txt')).toBe(true);
    expect(isSafeContentUrl('../file.txt')).toBe(true);
    expect(isSafeContentUrl('#section')).toBe(true);
    expect(isSafeContentUrl('//evil.example')).toBe(false);
    expect(isSafeContentUrl('javascript:alert(1)')).toBe(false);
    expect(isSafeContentUrl('data:text/html,boom')).toBe(false);
    expect(isSafeContentUrl('file:///etc/passwd')).toBe(false);
  });

  it('removes only closed LanguageGUI fences for canonical de-duplication', () => {
    const markdown = [
      'before',
      '```languagegui',
      '{"version":"languagegui/v1","blocks":[]}',
      '```',
      'after',
      '```json',
      '{"keep":true}',
      '```',
    ].join('\n');
    expect(stripLanguageGuiFences(markdown)).toContain('before\n\nafter');
    expect(stripLanguageGuiFences(markdown)).toContain('```json');
    expect(stripLanguageGuiFences('```languagegui\n{"incomplete":true}')).toContain('languagegui');
  });
});
