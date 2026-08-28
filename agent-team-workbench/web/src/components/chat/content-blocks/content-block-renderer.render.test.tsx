import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { parseContentBlockDocument } from '../../../utils/content-blocks';
import { ContentBlockList } from './content-block-renderer';
import { ChartBlock } from './chart-block';

describe('ContentBlockList', () => {
  it('renders metric, table, chart, file and event with semantic markup', () => {
    const document = parseContentBlockDocument({
      version: 'languagegui/v1',
      blocks: [
        {
          type: 'metric',
          title: '关键指标',
          source: { label: 'Finance API', url: 'https://example.com/source' },
          items: [
            { label: 'Revenue', value: '$2,480.58', delta: '+12%', tone: 'positive', detail: 'vs last month' },
            { label: 'Burn', value: '$840', delta: '-4%', tone: 'negative' },
          ],
        },
        {
          type: 'table',
          title: 'Top markets',
          columns: [{ key: 'name', label: 'Market' }, { key: 'value', label: 'Value', align: 'right' }],
          rows: [{ name: '<script>alert(1)</script>', value: 42 }],
        },
        {
          type: 'chart',
          title: 'Trend',
          chart: 'line',
          labels: ['Mon', 'Tue'],
          series: [{ name: 'Revenue', values: [10, 14] }],
          unit: 'k',
        },
        {
          type: 'file',
          title: 'Artifacts',
          files: [{ name: 'report.csv', mime: 'text/csv', size: '24 KB', status: 'ready', url: '/files/report.csv' }],
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
        { type: 'image', title: 'Screenshots', images: [{ src: '/images/chart.png', alt: 'Chart', caption: 'Result' }] },
        { type: 'audio', title: 'Briefing', tracks: [{ src: 'https://example.com/briefing.mp3', title: 'Voice briefing', duration: '02:18' }] },
        { type: 'map', title: 'Location', location: 'Shanghai', latitude: 31.2304, longitude: 121.4737, url: 'https://www.openstreetmap.org/' },
        { type: 'search', query: 'LanguageGUI', results: [{ title: 'LanguageGUI', url: 'https://languagegui.com/', snippet: '<script>plain text</script>', source: 'languagegui.com' }] },
        { type: 'rating', question: 'Helpful?', low_label: 'No', high_label: 'Very' },
        {
          type: 'review-summary', verdict: 'passed_with_warnings', summary: '检查完成，仍有注意事项。',
          stats: { files: 2, findings: 1, passed: 1 },
          findings: [{ severity: 'medium', title: '注意项', detail: '需要复核', file: 'runner.go', line: 214, evidence: 'go test ./...', suggestion: '补齐断言' }],
          checks: [{ label: 'Tests', status: 'passed' }, { label: 'CI', status: 'warning' }],
          next_steps: [{ label: '继续复核' }],
        },
        {
          type: 'canvas',
          title: 'Onboarding',
          nodes: [
            { id: 'start', label: 'Sign up', kind: 'start' },
            { id: 'verify', label: 'Verify email', kind: 'process' },
            { id: 'done', label: 'Dashboard', kind: 'end' },
          ],
          edges: [{ from: 'start', to: 'verify' }, { from: 'verify', to: 'done', label: 'ok' }],
        },
      ],
    });
    if (!document) throw new Error('fixture must parse');
    const html = renderToStaticMarkup(<ContentBlockList document={document} />);

    expect(html).toContain('data-content-block-version="languagegui/v1"');
    for (const type of ['metric', 'table', 'chart', 'file', 'event', 'image', 'audio', 'map', 'search', 'rating', 'review-summary', 'canvas']) {
      expect(html).toContain(`data-content-block="${type}"`);
    }
    expect(html).toContain('<dl class="chat-content-metric-grid">');
    expect(html).toContain('<table class="chat-content-table">');
    expect(html).toContain('正在加载图表');
    expect(html).toContain('aria-label="文件列表"');
    expect(html).toContain('aria-label="2026 年 8 月 28 日"');
    expect(html).toContain('alt="Chart"');
    expect(html).toContain('aria-label="播放音频：Voice briefing"');
    expect(html).toContain('31.2304, 121.4737');
    expect(html).toContain('aria-label="搜索结果"');
    expect(html).toContain('role="radiogroup" aria-label="Helpful?"');
    expect(html).toContain('评审结果');
    expect(html).toContain('检查完成，仍有注意事项。');
    expect(html).toContain('chat-review-stats');
    expect(html).toContain('runner.go:214');
    expect(html).toContain('1/2 通过');
    expect(html).toContain('验证结果');
    expect(html).toContain('下一步');
    expect(html).toContain('chat-content-canvas');
    expect(html).toContain('Sign up');
    expect(html.match(/role="radio"/g)).toHaveLength(5);
    expect(html).toContain('&lt;script&gt;plain text&lt;/script&gt;');
    expect(html).toContain('target="_blank" rel="noreferrer noopener"');
    expect(html).toContain('&lt;script&gt;alert(1)&lt;/script&gt;');
    expect(html).not.toContain('<script>');
  });

  it('renders the lazy chart implementation with an accessible summary and data table', () => {
    const document = parseContentBlockDocument({
      version: 'languagegui/v1',
      blocks: [{ type: 'chart', title: 'Trend', chart: 'line', labels: ['Mon', 'Tue'], series: [{ name: 'Revenue', values: [10, 14] }], unit: 'k' }],
    });
    const block = document?.blocks[0];
    if (!block || block.type !== 'chart') throw new Error('chart fixture must parse');
    const html = renderToStaticMarkup(<ChartBlock block={block} />);
    expect(html).toContain('role="img" aria-label="Trend，Revenue 共 2 个数据点"');
    expect(html).toContain('查看数据表');
    expect(html).toContain('<th scope="row">Mon</th>');
  });

  it('does not create file or event actions without a safe URL', () => {
    const document = parseContentBlockDocument({
      version: 'languagegui/v1',
      blocks: [
        { type: 'file', files: [{ name: 'unsafe.html', url: 'javascript:alert(1)' }] },
        { type: 'event', title: 'Unsafe', start: 'tomorrow', url: 'data:text/html,bad' },
      ],
    });
    if (!document) throw new Error('fixture must parse');
    const html = renderToStaticMarkup(<ContentBlockList document={document} />);
    expect(html).not.toContain('href=');
    expect(html).not.toContain('javascript:');
    expect(html).not.toContain('data:text/html');
  });
});
