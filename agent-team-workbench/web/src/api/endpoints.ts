import { apiFetch } from './client';
import type {
  Activity,
  AgentPolicy,
  AgentProfile,
  ApprovalRequest,
  Artifact,
  Bootstrap,
  Dashboard,
  DSHCatalog,
  ExecutionRun,
  Me,
  ModelEntry,
  Priority,
  ProbeResult,
  RunEvent,
  RuntimeBinding,
  TaskSession,
  WakeResult,
  WorkItem,
  WorkItemStatus,
  Workspace,
} from './types';

/**
 * 只封装控制平面已实现的端点（internal/httpapi/server.go）。
 */

// ── Workspace / Bootstrap / Dashboard / Activities ──────────────────

export const getMe = () => apiFetch<Me>('/me');

export const listWorkspaces = () => apiFetch<{ items: Workspace[] }>('/workspaces');

/** 更新 Workspace 名称/时区（Owner/Admin；乐观锁）。 */
export const patchWorkspace = (workspaceId: string, input: { name?: string; timezone?: string; expected_version: number }) =>
  apiFetch<Workspace>(`/workspaces/${workspaceId}`, { method: 'PATCH', body: input });

export const getBootstrap = (workspaceId: string) =>
  apiFetch<Bootstrap>(`/workspaces/${workspaceId}/bootstrap`);

export const getDashboard = (workspaceId: string) =>
  apiFetch<Dashboard>(`/workspaces/${workspaceId}/dashboard`);

export const listActivities = (workspaceId: string) =>
  apiFetch<{ items: Activity[]; next_cursor: string | null }>(`/workspaces/${workspaceId}/activities`);

// ── AgentProfile ────────────────────────────────────────────────────

export const listAgents = (workspaceId: string) =>
  apiFetch<{ items: AgentProfile[] }>(`/workspaces/${workspaceId}/agent-profiles`);

export interface CreateAgentInput {
  name: string;
  role: string;
  skills: string[];
  avatar?: string;
  runtime_preference?: { preferred: string; fallbacks?: string[]; mode?: 'default' | 'plan'; agent_preset?: string };
}

export const createAgent = (workspaceId: string, input: CreateAgentInput) =>
  apiFetch<AgentProfile>(`/workspaces/${workspaceId}/agent-profiles`, { method: 'POST', body: input });

export const enableAgent = (agentId: string) =>
  apiFetch<AgentProfile>(`/agent-profiles/${agentId}/commands/enable`, { method: 'POST', body: {} });

export const disableAgent = (agentId: string) =>
  apiFetch<AgentProfile>(`/agent-profiles/${agentId}/commands/disable`, { method: 'POST', body: {} });

// ── WorkItem ────────────────────────────────────────────────────────

export interface WorkItemFilter {
  status?: WorkItemStatus;
  priority?: Priority;
  assignee?: string;
  cursor?: string;
}

export const listWorkItems = (workspaceId: string, filter: WorkItemFilter = {}) => {
  const params = new URLSearchParams();
  if (filter.status) params.set('status', filter.status);
  if (filter.priority) params.set('priority', filter.priority);
  if (filter.assignee) params.set('assignee', filter.assignee);
  if (filter.cursor) params.set('cursor', filter.cursor);
  const qs = params.toString();
  return apiFetch<{ items: WorkItem[]; next_cursor: string | null }>(
    `/workspaces/${workspaceId}/work-items${qs ? `?${qs}` : ''}`,
  );
};

export const getWorkItem = (workItemId: string) => apiFetch<WorkItem>(`/work-items/${workItemId}`);

/** 更新任务普通字段（标题/描述/优先级/截止日；状态走 commands）。 */
export const patchWorkItem = (
  workItemId: string,
  input: { title?: string; description?: string; priority?: Priority; due_date?: string | null; expected_version: number },
) => apiFetch<WorkItem>(`/work-items/${workItemId}`, { method: 'PATCH', body: input });

export interface CreateWorkItemInput {
  title: string;
  description?: string;
  status?: WorkItemStatus;
  priority?: Priority;
  due_date?: string | null;
  agent_profile_id?: string;
}

export const createWorkItem = (workspaceId: string, input: CreateWorkItemInput) =>
  apiFetch<WorkItem>(`/workspaces/${workspaceId}/work-items`, { method: 'POST', body: input });

export const moveWorkItem = (workItemId: string, status: WorkItemStatus, expectedVersion: number) =>
  apiFetch<WorkItem>(`/work-items/${workItemId}/commands/move`, {
    method: 'POST',
    body: { status, expected_version: expectedVersion },
  });

export const assignWorkItem = (workItemId: string, agentProfileId: string, expectedVersion: number) =>
  apiFetch<WorkItem>(`/work-items/${workItemId}/commands/assign`, {
    method: 'POST',
    body: { agent_profile_id: agentProfileId, expected_version: expectedVersion },
  });

export const blockWorkItem = (
  workItemId: string,
  blocker: { code: string; message: string; source: string },
  expectedVersion: number,
) =>
  apiFetch<WorkItem>(`/work-items/${workItemId}/commands/block`, {
    method: 'POST',
    body: { ...blocker, expected_version: expectedVersion },
  });

export const unblockWorkItem = (workItemId: string, expectedVersion: number) =>
  apiFetch<WorkItem>(`/work-items/${workItemId}/commands/unblock`, {
    method: 'POST',
    body: { expected_version: expectedVersion },
  });

export const acceptWorkItem = (workItemId: string, expectedVersion: number) =>
  apiFetch<WorkItem>(`/work-items/${workItemId}/commands/accept`, {
    method: 'POST',
    body: { expected_version: expectedVersion },
  });

// ── Run / Approval / Artifact ───────────────────────────────────────

export interface CreateRunInput {
  agent_profile_id?: string;
  runtime_preference?: { preferred: string; fallbacks?: string[] };
  requirements?: Record<string, string>;
  input: { instruction: string; acceptance_criteria?: string[] };
}

/** 成功返回 202；真正开始执行由 SSE 事件确认（协议 §5.4）。 */
export const createRun = (workItemId: string, input: CreateRunInput) =>
  apiFetch<{
    run_id: string;
    work_item_id: string;
    status: string;
    version: number;
    capability_snapshot_id: string | null;
  }>(`/work-items/${workItemId}/runs`, { method: 'POST', body: input });

export const getRun = (runId: string) => apiFetch<ExecutionRun>(`/runs/${runId}`);

export const interruptRun = (runId: string) =>
  apiFetch<ExecutionRun>(`/runs/${runId}/commands/interrupt`, { method: 'POST', body: {} });

export const cancelRun = (runId: string) =>
  apiFetch<ExecutionRun>(`/runs/${runId}/commands/cancel`, { method: 'POST', body: {} });

export const retryRun = (runId: string) =>
  apiFetch<ExecutionRun>(`/runs/${runId}/commands/retry`, { method: 'POST', body: {} });

/** 恢复 reconnecting/lost 的 Run（仅当 Runtime 能力声明 resume=supported，否则 422）。 */
export const resumeRun = (runId: string) =>
  apiFetch<ExecutionRun>(`/runs/${runId}/commands/resume`, { method: 'POST', body: {} });

export const listApprovals = (runId: string) =>
  apiFetch<{ items: ApprovalRequest[] }>(`/runs/${runId}/approvals`);

export const resolveApproval = (approvalId: string, runId: string, decision: 'approved' | 'rejected', reason?: string) =>
  apiFetch<ApprovalRequest>(`/runs/${runId}/approvals/${approvalId}/commands/resolve`, {
    method: 'POST',
    body: { decision, reason: reason ?? '' },
  });

export const listArtifacts = (runId: string) =>
  apiFetch<{ items: Artifact[] }>(`/runs/${runId}/artifacts`);

/** 回放 Run 事件历史（对话页；只读投影，按 run_seq 排序）。 */
export const listRunEvents = (runId: string) =>
  apiFetch<{ items: RunEvent[] }>(`/runs/${runId}/events`);

/** 一个任务的全部 Run（对话轮次历史）。 */
export const listWorkItemRuns = (workItemId: string) =>
  apiFetch<{ items: ExecutionRun[] }>(`/work-items/${workItemId}/runs`);

/** 向活动 Run 追加用户输入 / steering（协议 §5.3）。 */
export const sendRunInput = (runId: string, instruction: string) =>
  apiFetch<{ accepted: boolean }>(`/runs/${runId}/commands/input`, {
    method: 'POST',
    body: { instruction },
  });

// ── Agent 配置（agents/ 目录为真相源）──────────────────────────────

export interface PatchAgentInput {
  name?: string;
  role?: string;
  skills?: string[];
  instructions?: string;
  runtime_preference?: { preferred?: string; fallbacks?: string[]; mode?: 'default' | 'plan'; agent_preset?: string };
  model_override?: { ref?: string; provider?: string; model?: string };
  policy?: AgentPolicy;
  expected_version: number;
}

/** 修改 Agent 配置；成功后服务端回写 agents/<slug>/ 文件。 */
export const patchAgent = (agentId: string, input: PatchAgentInput) =>
  apiFetch<AgentProfile>(`/agent-profiles/${agentId}`, { method: 'PATCH', body: input });

/** 手动重扫 agents/ 配置目录并导入 DB 投影。 */
export const reloadAgentConfigs = (workspaceId: string) =>
  apiFetch<{ created: number; updated: number; skipped: number }>(
    `/workspaces/${workspaceId}/agent-config/reload`,
    { method: 'POST', body: {} },
  );

// ── RuntimeBinding（设置页）─────────────────────────────────────────

export const listRuntimeBindings = (workspaceId: string) =>
  apiFetch<{ items: RuntimeBinding[] }>(`/workspaces/${workspaceId}/runtime-bindings`);

/** DeepSeek Harness 模式与权限预设（来自本地 preset 目录扫描）。 */
export const getDSHCatalog = () => apiFetch<DSHCatalog>('/runtimes/dsh/catalog');

/** 探测版本/认证/能力；不启动业务 Run（协议 §5.3）。 */
export const probeRuntimeBinding = (bindingId: string) =>
  apiFetch<ProbeResult>(`/runtime-bindings/${bindingId}/commands/probe`, { method: 'POST', body: {} });

export interface UpsertBindingInput {
  runtime_label?: string;
  adapter_id?: string;
  provider?: string;
  model?: string;
  credential_ref?: string;
  expected_version?: number;
}

/** 新建 Runtime 绑定（凭据只存引用）。 */
export const createRuntimeBinding = (workspaceId: string, input: UpsertBindingInput) =>
  apiFetch<RuntimeBinding>(`/workspaces/${workspaceId}/runtime-bindings`, { method: 'POST', body: input });

/** 修改绑定的 provider/model/credential_ref（乐观锁）。 */
export const patchRuntimeBinding = (bindingId: string, input: UpsertBindingInput) =>
  apiFetch<RuntimeBinding>(`/runtime-bindings/${bindingId}`, { method: 'PATCH', body: input });

// ── 模型注册表（models/ 目录为真相源，全局共享）──────────────────────

export const listModels = () => apiFetch<{ items: ModelEntry[] }>('/models');

export interface UpsertModelInput {
  id?: string;
  display_name: string;
  category?: string;
  provider_id?: string;
  provider: string;
  api?: string;
  model: string;
  api_key_env?: string;
  base_url?: string;
  context_window?: number;
  max_tokens?: number;
  notes?: string;
}

export const createModel = (input: UpsertModelInput) =>
  apiFetch<ModelEntry>('/models', { method: 'POST', body: input });

export const updateModel = (id: string, input: UpsertModelInput) =>
  apiFetch<ModelEntry>(`/models/${id}`, { method: 'PUT', body: input });

export const deleteModel = (id: string) => apiFetch<void>(`/models/${id}`, { method: 'DELETE' });

export const getProviderCredential = (providerId: string) =>
  apiFetch<{ api_key: string }>(`/models/provider-credentials?provider_id=${encodeURIComponent(providerId)}`);

export const putProviderCredential = (providerId: string, apiKey: string) =>
  apiFetch<void>('/models/provider-credentials', { method: 'PUT', body: { provider_id: providerId, api_key: apiKey } });

// ── 会话锚点 / 手动唤醒（M4/M5 wakeup 调度）──────────────────────────

/** Agent 的活跃会话锚点列表（墓碑行服务端已过滤）。 */
export const listTaskSessions = (agentId: string) =>
  apiFetch<{ items: TaskSession[] }>(`/agent-profiles/${agentId}/task-sessions`);

/** 重置指定 (adapter, task_key) 的会话锚点（写墓碑，下一轮开全新会话）。 */
export const resetTaskSession = (agentId: string, input: { task_key: string; adapter_id: string }) =>
  apiFetch<{ status: string }>(`/agent-profiles/${agentId}/task-sessions/reset`, { method: 'POST', body: input });

/** on_demand 手动唤醒；task_key 锚定 work item。agent 未开 wake_on_demand 时 422。 */
export const wakeAgent = (agentId: string, input: { task_key: string; instruction?: string }) =>
  apiFetch<WakeResult>(`/agent-profiles/${agentId}/commands/wake`, { method: 'POST', body: input });
