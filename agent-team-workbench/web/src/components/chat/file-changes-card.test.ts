import { beforeEach, describe, expect, it } from 'vitest';
import { captureScope } from '../../stores/scope';
import { useWorkspaceStore } from '../../stores/workspace.store';
import { createRunChangesRequestFence } from './file-changes-card';

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

beforeEach(() => {
  useWorkspaceStore.setState({
    workspace: { id: 'ws_1', name: '甲', timezone: 'UTC', version: 1 },
    selectedWorkspaceId: 'ws_1',
    generation: 4,
  });
});

describe('FileChangesCard refresh fence', () => {
  it('回滚 SSE 触发的第二次请求先完成时，初始慢响应不能把 reverted 覆盖回 ready', async () => {
    const fence = createRunChangesRequestFence();
    const first = deferred<'ready'>();
    const reloaded = deferred<'reverted'>();
    let shown: 'idle' | 'ready' | 'reverted' = 'idle';

    const initialScope = captureScope();
    const initialRequest = fence.begin();
    const initialWrite = first.promise.then((value) => {
      if (fence.accepts(initialRequest, initialScope)) shown = value;
    });

    // file_changes.reverted 推进 changesRevision 后，卡片发起新的权威重拉。
    const replayScope = captureScope();
    const replayRequest = fence.begin();
    const replayWrite = reloaded.promise.then((value) => {
      if (fence.accepts(replayRequest, replayScope)) shown = value;
    });

    reloaded.resolve('reverted');
    await replayWrite;
    expect(shown).toBe('reverted');

    first.resolve('ready');
    await initialWrite;
    expect(shown).toBe('reverted');
  });

  it('Workspace generation 改变后，即使请求后返回也拒绝写入', () => {
    const fence = createRunChangesRequestFence();
    const scope = captureScope();
    const request = fence.begin();

    useWorkspaceStore.setState((state) => ({ generation: state.generation + 1 }));

    expect(fence.accepts(request, scope)).toBe(false);
  });
});
