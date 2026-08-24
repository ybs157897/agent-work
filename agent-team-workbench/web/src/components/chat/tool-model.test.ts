import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../../stores/chat.store';
import { FAMILY_TITLES, classifyTool, firstLine, looksLikePath, toolRowModel } from './tool-model';

const msg = (over: Partial<ChatMessage> & Pick<ChatMessage, 'key' | 'runId' | 'kind' | 'text' | 'at'>): ChatMessage =>
  over;

describe('classifyTool', () => {
  it('精确表命中（大小写不敏感）', () => {
    expect(classifyTool('bash')).toBe('bash');
    expect(classifyTool('Bash')).toBe('bash');
    expect(classifyTool('SHELL')).toBe('bash');
    expect(classifyTool('pwsh')).toBe('bash');
    expect(classifyTool('zsh')).toBe('bash');
    expect(classifyTool('Terminal')).toBe('bash');
    expect(classifyTool('Read')).toBe('read');
    expect(classifyTool('cat')).toBe('read');
    expect(classifyTool('VIEW')).toBe('read');
    expect(classifyTool('web_fetch')).toBe('read');
    expect(classifyTool('Write')).toBe('write');
    expect(classifyTool('create_file')).toBe('write');
    expect(classifyTool('Edit')).toBe('edit');
    expect(classifyTool('patch')).toBe('edit');
    expect(classifyTool('apply_patch')).toBe('edit');
    expect(classifyTool('grep')).toBe('search');
    expect(classifyTool('Glob')).toBe('search');
    expect(classifyTool('search')).toBe('search');
    expect(classifyTool('web_search')).toBe('search');
    expect(classifyTool('find')).toBe('search');
    expect(classifyTool('run_code')).toBe('code');
    expect(classifyTool('python')).toBe('code');
    expect(classifyTool('execute_code')).toBe('code');
  });

  it('正则兜底：未知名按关键词归类', () => {
    expect(classifyTool('run_shell_command')).toBe('bash');
    expect(classifyTool('execute_command')).toBe('bash');
    expect(classifyTool('view_file')).toBe('read');
    expect(classifyTool('find_files')).toBe('search');
    expect(classifyTool('web_query')).toBe('search');
    expect(classifyTool('run_python_repl')).toBe('code');
    expect(classifyTool('code')).toBe('code'); // ^code$ 精确兜底
  });

  it('词边界兜底防误伤：ReadFile/codex 不因含词根误入族', () => {
    expect(classifyTool('ReadFile')).toBe('others');
    expect(classifyTool('codex_agent')).toBe('others');
    expect(classifyTool('task')).toBe('others');
    expect(classifyTool('browser_navigate')).toBe('others');
    expect(classifyTool('')).toBe('others');
    expect(classifyTool(undefined)).toBe('others');
  });

  it('FAMILY_TITLES 覆盖全族', () => {
    expect(Object.keys(FAMILY_TITLES).sort()).toEqual(['bash', 'code', 'edit', 'others', 'read', 'search', 'write']);
  });
});

describe('firstLine / looksLikePath', () => {
  it('firstLine 取首个换行前内容', () => {
    expect(firstLine('a\nb\nc')).toBe('a');
    expect(firstLine('only')).toBe('only');
    expect(firstLine('')).toBe('');
  });

  it('looksLikePath：含 /、~ 前缀或常见扩展名', () => {
    expect(looksLikePath('/abs/path/file.ts')).toBe(true);
    expect(looksLikePath('src/app/main.go')).toBe(true);
    expect(looksLikePath('~/config')).toBe(true);
    expect(looksLikePath('main.py')).toBe(true);
    expect(looksLikePath('hello world')).toBe(false);
    expect(looksLikePath('  ')).toBe(false);
    expect(looksLikePath('')).toBe(false);
  });
});

describe('toolRowModel · summary', () => {
  it('argsSummary 首行优先（多行只取首行）', () => {
    const m = toolRowModel(
      msg({
        key: 't',
        runId: 'r',
        kind: 'tool',
        at: '',
        tool: 'Bash',
        toolStatus: 'running',
        text: '调用工具 Bash：go test ./...',
        argsSummary: 'go test ./...\n-covermode=atomic',
      }),
    );
    expect(m.summary).toBe('go test ./...');
  });

  it('无 argsSummary 时剥工具名前缀（全角冒号）取首行', () => {
    const m = toolRowModel(
      msg({ key: 't', runId: 'r', kind: 'tool', at: '', tool: 'Read', toolStatus: 'success', text: '调用工具 Read：src/index.ts' }),
    );
    expect(m.summary).toBe('src/index.ts');
  });

  it('通用正则剥前缀：半角冒号/工具名不匹配时兜底', () => {
    const m = toolRowModel(
      msg({ key: 't', runId: 'r', kind: 'tool', at: '', text: '调用工具 read: ls -la' }),
    );
    expect(m.summary).toBe('ls -la');
  });

  it('工具输出前缀剥除；text 多行取首行', () => {
    const out = toolRowModel(
      msg({ key: 't', runId: 'r', kind: 'tool', at: '', toolStatus: 'success', text: '工具输出', detail: 'ok' }),
    );
    expect(out.summary).toBe('');
    const multi = toolRowModel(
      msg({ key: 't2', runId: 'r', kind: 'tool', at: '', tool: 'Write', text: '调用工具 Write：多行摘要\n第二行' }),
    );
    expect(multi.summary).toBe('多行摘要');
  });
});

describe('toolRowModel · state', () => {
  const base = { key: 't', runId: 'r', kind: 'tool' as const, at: '', tool: 'Bash' };

  it('running → running（running=true）', () => {
    const m = toolRowModel(msg({ ...base, text: '调用工具 Bash：x', toolStatus: 'running' }));
    expect(m.state).toBe('running');
    expect(m.running).toBe(true);
  });

  it('failed → error', () => {
    const m = toolRowModel(msg({ ...base, text: '工具失败 Bash：x', toolStatus: 'failed', detail: 'boom' }));
    expect(m.state).toBe('error');
    expect(m.running).toBe(false);
  });

  it('success 且 exitCode=0 → ok；无 toolStatus 也 → ok', () => {
    expect(toolRowModel(msg({ ...base, text: 'x', toolStatus: 'success', exitCode: 0 })).state).toBe('ok');
    expect(toolRowModel(msg({ ...base, text: 'x' })).state).toBe('ok');
  });

  it('success 但 exitCode≠0 → error（终端语义）且 exitCode 透传', () => {
    const m = toolRowModel(msg({ ...base, text: 'x · exit 2', toolStatus: 'success', exitCode: 2, detail: 'boom\ntrace' }));
    expect(m.state).toBe('error');
    expect(m.exitCode).toBe(2);
    expect(m.errorSummary).toBe('boom');
  });
});

describe('toolRowModel · body/output/errorSummary/filePath', () => {
  it('body：args pretty JSON；非 JSON 原文；缺失为 null', () => {
    const pretty = toolRowModel(
      msg({ key: 't', runId: 'r', kind: 'tool', at: '', tool: 'Write', text: 'x', args: '{"path":"a.ts","content":"x"}' }),
    );
    expect(pretty.body).toBe(JSON.stringify({ path: 'a.ts', content: 'x' }, null, 2));
    expect(pretty.filePath).toBe('a.ts');
    const raw = toolRowModel(
      msg({ key: 't2', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', args: '{"command":"tru' }),
    );
    expect(raw.body).toBe('{"command":"tru');
    expect(toolRowModel(msg({ key: 't3', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x' })).body).toBeNull();
  });

  it('output：detail 优先；detail 空白回退 liveOutput；均无为 null', () => {
    const detail = toolRowModel(msg({ key: 't', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', detail: 'done', liveOutput: 'partial' }));
    expect(detail.output).toBe('done');
    const live = toolRowModel(msg({ key: 't2', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', toolStatus: 'running', detail: '   ', liveOutput: 'partial' }));
    expect(live.output).toBe('partial');
    expect(toolRowModel(msg({ key: 't3', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x' })).output).toBeNull();
  });

  it('errorSummary：error 态取 output 首行；ok/running 态或无输出为 null', () => {
    const err = toolRowModel(msg({ key: 't', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', toolStatus: 'failed', detail: 'line1\nline2' }));
    expect(err.errorSummary).toBe('line1');
    const ok = toolRowModel(msg({ key: 't2', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', toolStatus: 'success', detail: 'fine' }));
    expect(ok.errorSummary).toBeNull();
    const noOut = toolRowModel(msg({ key: 't3', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', toolStatus: 'failed' }));
    expect(noOut.errorSummary).toBeNull();
  });

  it('filePath：仅 read/write/edit 族；args JSON 键优先，其次 argsSummary 路径启发式', () => {
    expect(
      toolRowModel(msg({ key: 't', runId: 'r', kind: 'tool', at: '', tool: 'Read', text: 'x', args: '{"file_path":"src/y.go"}' })).filePath,
    ).toBe('src/y.go');
    expect(
      toolRowModel(msg({ key: 't2', runId: 'r', kind: 'tool', at: '', tool: 'Edit', text: 'x', argsSummary: 'src/z.py' })).filePath,
    ).toBe('src/z.py');
    // 非路径形态的 argsSummary 不当路径；非文件族一律 undefined。
    expect(
      toolRowModel(msg({ key: 't3', runId: 'r', kind: 'tool', at: '', tool: 'Read', text: 'x', argsSummary: '一段描述文字' })).filePath,
    ).toBeUndefined();
    expect(
      toolRowModel(msg({ key: 't4', runId: 'r', kind: 'tool', at: '', tool: 'Bash', text: 'x', argsSummary: '/tmp/x.sh' })).filePath,
    ).toBeUndefined();
  });

  it('title 取 FAMILY_TITLES；family 与归类一致', () => {
    const m = toolRowModel(msg({ key: 't', runId: 'r', kind: 'tool', at: '', tool: 'grep', text: 'x' }));
    expect(m.family).toBe('search');
    expect(m.title).toBe('Search');
  });
});
