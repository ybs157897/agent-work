import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import type { ExecutionRun } from '../../api/types';
import { RunErrorBanner } from './run-error-banner';

const run = (overrides: Partial<ExecutionRun> = {}): ExecutionRun => ({
  id: 'run-failed',
  work_item_id: 'wi-1',
  status: 'failed',
  failure: { code: 'MISSING_CREDENTIAL', message: 'no API key', retryable: true },
  version: 1,
  created_at: '2026-08-28T00:00:00Z',
  updated_at: '2026-08-28T00:00:01Z',
  ...overrides,
});

describe('RunErrorBanner', () => {
  it('把运行错误渲染在可展开、可复制、可重试的 banner，而不是 transcript 行', () => {
    const html = renderToStaticMarkup(<RunErrorBanner run={run()} onRetry={async () => undefined} />);
    expect(html).toContain('MISSING_CREDENTIAL: no API key');
    expect(html).toContain('查看详情');
    expect(html).toContain('复制错误信息');
    expect(html).toContain('重试');
    expect(html).toContain('role="alert"');
  });

  it('没有 failure 详情时安全回退，且取消类状态不显示', () => {
    expect(renderToStaticMarkup(<RunErrorBanner run={run({ failure: undefined })} onRetry={async () => undefined} />)).toContain('运行失败');
    expect(renderToStaticMarkup(<RunErrorBanner run={run({ status: 'cancelled' })} onRetry={async () => undefined} />)).toBe('');
    expect(renderToStaticMarkup(<RunErrorBanner run={run({ status: 'interrupted' })} onRetry={async () => undefined} />)).toBe('');
  });

  it('主文案最多显示 500 字符，完整错误仍留在详情数据中', () => {
    const html = renderToStaticMarkup(<RunErrorBanner
      run={run({ failure: { code: 'provider_error', message: `HEAD-${'x'.repeat(600)}-TAIL`, retryable: false } })}
      onRetry={async () => undefined}
    />);
    expect(html).toContain('HEAD-');
    expect(html).toContain('…');
    expect(html).not.toContain('-TAIL</p>');
    expect(html).not.toContain('aria-controls');
  });
});
