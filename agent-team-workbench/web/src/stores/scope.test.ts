import { beforeEach, describe, expect, it } from 'vitest';
import { captureScope, isCurrentWorkspaceEntity } from './scope';
import { useWorkspaceStore } from './workspace.store';

beforeEach(() => {
  useWorkspaceStore.setState({
    workspace: { id: 'ws_1', name: '甲', timezone: 'UTC', version: 1 },
    selectedWorkspaceId: 'ws_1',
    generation: 7,
  });
});

describe('isCurrentWorkspaceEntity', () => {
  it('同时约束当前 generation 与实体 workspace_id，裸资源响应不能跨 Workspace 接纳', () => {
    const scope = captureScope();

    expect(isCurrentWorkspaceEntity(scope, { workspace_id: 'ws_1' })).toBe(true);
    expect(isCurrentWorkspaceEntity(scope, { workspace_id: 'ws_2' })).toBe(false);

    useWorkspaceStore.setState((state) => ({ generation: state.generation + 1 }));
    expect(isCurrentWorkspaceEntity(scope, { workspace_id: 'ws_1' })).toBe(false);
  });
});
