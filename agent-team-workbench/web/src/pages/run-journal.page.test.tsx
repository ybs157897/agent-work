import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import { getRunJournal } from '../api/endpoints';
import type { RunJournal, RunJournalPhase } from '../api/types';
import {
  RunJournalView,
  formatDuration,
  groupJournalPhases,
  toJournalViewState,
} from './run-journal.page';

/**
 * Run 环节时间线渲染测试：fetch 一律 mock，不依赖真后端；
 * 契约字段与冻结的 GET /api/v1/runs/{run_id}/journal 逐字段对齐。
 */

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const problem = (status: number, code: string) =>
  new Response(JSON.stringify({ type: 'about:blank', title: 'x', status, code }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });

function phase(extra: Partial<RunJournalPhase> & { phase: RunJournalPhase['phase'] }): RunJournalPhase {
  return {
    attempt: 1,
    entered_at: '2026-09-03T01:00:00Z',
    closed_at: '2026-09-03T01:00:00Z',
    outcome: 'ok',
    duration_ms: 12,
    failure: null,
    detail: null,
    ...extra,
  };
}

const allOkJournal: RunJournal = {
  run_id: 'run_ok',
  generated_at: '2026-09-03T01:00:05Z',
  phases: [
    phase({ phase: 'dispatch' }),
    phase({ phase: 'spawn', duration_ms: 45 }),
    phase({ phase: 'handshake', duration_ms: 7 }),
    phase({ phase: 'first_event', duration_ms: 130 }),
    phase({ phase: 'streaming', duration_ms: 4200 }),
    phase({ phase: 'settle', duration_ms: 9 }),
    phase({
      phase: 'post',
      detail: { hook: 'maybeAdvancePlans', work_item_id: 'wi_1' },
    }),
  ],
  log: { chunks: 17, truncated: false },
  governance: { goal_id: 'goal_1', todo_id: 'todo_9', turn_seq: 3, digest: 'sha256:ab12' },
};

describe('RunJournalView · 全链正常', () => {
  const html = renderToStaticMarkup(
    <RunJournalView state={{ kind: 'ready', journal: allOkJournal }} onRetry={() => {}} />,
  );

  it('七段相位全部以中文名出现', () => {
    expect(html).toContain('派发');
    expect(html).toContain('拉起进程');
    expect(html).toContain('握手 / resume');
    expect(html).toContain('等待首帧');
    expect(html).toContain('对话流');
    expect(html).toContain('终态裁决');
    expect(html).toContain('终态钩子');
  });

  it('outcome=ok 标记为正常，无故障指向条', () => {
    expect(html.match(/正常/g)).toHaveLength(7);
    expect(html).not.toContain('故障环节');
    expect(html).not.toContain('失败');
  });

  it('展示时长、attempt 与 log 摘要', () => {
    expect(html).toContain('12ms');
    expect(html).toContain('4.20s');
    expect(html).toContain('第 1 次');
    expect(html).toContain('共 17 条');
    expect(html).not.toContain('已截断');
  });

  it('governance 有值时渲染治理回合块', () => {
    expect(html).toContain('治理回合');
    expect(html).toContain('goal_1');
    expect(html).toContain('todo_9');
    expect(html).toContain('第 3 轮');
    expect(html).toContain('sha256:ab12');
  });
});

describe('RunJournalView · 含 failed 相位', () => {
  const journal: RunJournal = {
    ...allOkJournal,
    run_id: 'run_failed',
    phases: [
      phase({ phase: 'dispatch' }),
      phase({
        phase: 'streaming',
        closed_at: '2026-09-03T01:01:30Z',
        outcome: 'failed',
        duration_ms: 86_400,
        failure: {
          code: 'runner_timeout',
          message: 'runner 在 60s 内未回执心跳',
          family: 'deadline',
          retryable: true,
        },
      }),
    ],
  };
  const html = renderToStaticMarkup(
    <RunJournalView state={{ kind: 'ready', journal }} onRetry={() => {}} />,
  );

  it('失败证据默认展开：code / message / family / 可重试可见', () => {
    expect(html).toContain('runner_timeout');
    expect(html).toContain('runner 在 60s 内未回执心跳');
    expect(html).toContain('family=deadline');
    expect(html).toContain('可重试');
  });

  it('失败标记与故障指向条一眼可辨', () => {
    expect(html).toContain('失败');
    expect(html).toContain('故障环节');
    expect(html).toContain('对话流 · 第 1 次 · runner_timeout');
  });

  it('时长带分钟进位', () => {
    expect(html).toContain('1m26s');
  });
});

describe('RunJournalView · 含未闭合相位', () => {
  it('closed_at=null 标记为进行中，指向条落在最后一个未闭合相位', () => {
    const journal: RunJournal = {
      ...allOkJournal,
      run_id: 'run_open',
      governance: null,
      phases: [
        phase({ phase: 'dispatch' }),
        phase({ phase: 'settle', outcome: null, closed_at: null, duration_ms: null }),
      ],
    };
    const html = renderToStaticMarkup(
      <RunJournalView state={{ kind: 'ready', journal }} onRetry={() => {}} />,
    );
    expect(html).toContain('进行中');
    expect(html).toContain('进行中环节');
    expect(html).toContain('相位尚未闭合');
    expect(html).not.toContain('治理回合');
  });

  it('闭合但无 outcome 标记为中断', () => {
    const journal: RunJournal = {
      ...allOkJournal,
      run_id: 'run_lost',
      phases: [
        phase({ phase: 'dispatch' }),
        phase({ phase: 'streaming', outcome: null, duration_ms: null, closed_at: '2026-09-03T01:02:00Z' }),
      ],
    };
    const html = renderToStaticMarkup(
      <RunJournalView state={{ kind: 'ready', journal }} onRetry={() => {}} />,
    );
    expect(html).toContain('中断');
    expect(html).toContain('中断环节');
    expect(html).toContain('已闭合但未落到终态');
  });
});

describe('RunJournalView · post 相位多钩子分组', () => {
  const journal: RunJournal = {
    ...allOkJournal,
    phases: [
      phase({ phase: 'settle' }),
      phase({ phase: 'post', detail: { hook: 'maybeAdvancePlans' } }),
      phase({ phase: 'post', attempt: 2, detail: { hook: 'refreshDigest' } }),
    ],
  };
  const html = renderToStaticMarkup(
    <RunJournalView state={{ kind: 'ready', journal }} onRetry={() => {}} />,
  );

  it('同相位多条目归入一个相位组并标出段数', () => {
    expect(html).toContain('共 2 段');
  });

  it('每对钩子显示 hook 名与各自 attempt', () => {
    expect(html).toContain('maybeAdvancePlans');
    expect(html).toContain('refreshDigest');
    expect(html).toContain('第 2 次');
  });
});

describe('RunJournalView · 空态 / 加载态 / 404 态 / 错误态', () => {
  it('phases 为空时给出去向的空态', () => {
    const html = renderToStaticMarkup(
      <RunJournalView
        state={{ kind: 'ready', journal: { ...allOkJournal, phases: [] } }}
        onRetry={() => {}}
      />,
    );
    expect(html).toContain('该运行还没有环节记录');
    expect(html).not.toContain('运行环节时间线');
  });

  it('加载态是骨架占位', () => {
    const html = renderToStaticMarkup(<RunJournalView state={{ kind: 'loading' }} onRetry={() => {}} />);
    expect(html).toContain('环节时间线加载中');
    expect(html).toContain('aria-label="列表加载中"');
  });

  it('404 态说明记录不存在并给返回入口', () => {
    const html = renderToStaticMarkup(
      <MemoryRouter>
        <RunJournalView state={{ kind: 'not-found' }} onRetry={() => {}} />
      </MemoryRouter>,
    );
    expect(html).toContain('没有该运行的环节记录');
    expect(html).toContain('返回对话');
  });

  it('错误态展示消息并允许重试', () => {
    const html = renderToStaticMarkup(
      <RunJournalView state={{ kind: 'error', message: '控制面不可达' }} onRetry={() => {}} />,
    );
    expect(html).toContain('控制面不可达');
    expect(html).toContain('重试');
  });
});

describe('toJournalViewState（ApiError → 视图状态映射）', () => {
  it('404 是稳定事实，映射为 not-found', () => {
    const error = new ApiError({ type: 'about:blank', title: 'x', status: 404, code: 'not_found' });
    expect(toJournalViewState(error)).toEqual({ kind: 'not-found' });
  });

  it('其余错误保留消息，不当 404 吞掉', () => {
    const error = new ApiError({ type: 'about:blank', title: 'boom', status: 500, detail: 'db down' });
    expect(toJournalViewState(error)).toEqual({ kind: 'error', message: 'db down' });
    expect(toJournalViewState(new Error('network'))).toEqual({ kind: 'error', message: '环节时间线加载失败' });
  });
});

describe('getRunJournal（mock fetch 的端点契约）', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('GET /api/v1/runs/{run_id}/journal 并透传 journal 载荷', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(allOkJournal));
    vi.stubGlobal('fetch', fetchMock);

    const got = await getRunJournal('run_ok');
    expect(got).toEqual(allOkJournal);
    expect(got.phases[6].detail).toEqual({ hook: 'maybeAdvancePlans', work_item_id: 'wi_1' });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/runs/run_ok/journal');
    expect(init.method).toBe('GET');
  });

  it('problem+json 404 抛 ApiError，供页面映射 not-found 态', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(problem(404, 'not_found')));
    const error = await getRunJournal('run_missing').catch((e: unknown) => e);
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).status).toBe(404);
  });
});

describe('journal 投影辅助函数', () => {
  it('formatDuration 分档进位', () => {
    expect(formatDuration(12)).toBe('12ms');
    expect(formatDuration(4200)).toBe('4.20s');
    expect(formatDuration(86_400)).toBe('1m26s');
    expect(formatDuration(120_000)).toBe('2m');
  });

  it('groupJournalPhases 只合并连续同相位条目', () => {
    const groups = groupJournalPhases([
      phase({ phase: 'streaming' }),
      phase({ phase: 'streaming', attempt: 2 }),
      phase({ phase: 'post', detail: { hook: 'a' } }),
      phase({ phase: 'post', attempt: 2, detail: { hook: 'b' } }),
      phase({ phase: 'streaming', attempt: 2 }),
    ]);
    expect(groups.map((g) => `${g.name}:${g.entries.length}`)).toEqual([
      'streaming:2',
      'post:2',
      'streaming:1',
    ]);
  });
});
