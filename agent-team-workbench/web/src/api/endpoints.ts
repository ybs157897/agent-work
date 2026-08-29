import { apiFetch } from './client';
import type {
  Activity,
  AgentPolicy,
  AgentProfile,
  ApprovalRequest,
  Artifact,
  Bootstrap,
  Dashboard,
  DecisionEntry,
  DispatchCard,
  DSHCatalog,
  ExecutionRun,
  Me,
  ModelEntry,
  Plan,
  Priority,
  ProbeResult,
  RunEvent,
  RunChanges,
  RunChangeDiff,
  RuntimeBinding,
  SearchKind,
  SearchItem,
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
  /** 父任务过滤：任务 id 精确匹配；`'none'` 表示只看根任务（协议 §5.2）。 */
  parent_id?: string | 'none';
  cursor?: string;
}

export const listWorkItems = (workspaceId: string, filter: WorkItemFilter = {}) => {
  const params = new URLSearchParams();
  if (filter.status) params.set('status', filter.status);
  if (filter.priority) params.set('priority', filter.priority);
  if (filter.assignee) params.set('assignee', filter.assignee);
  if (filter.parent_id) params.set('parent_id', filter.parent_id);
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
  /** 作为子任务创建时指定；缺省为根任务。 */
  parent_id?: string;
  /** 实体级幂等键：同 workspace 下同 key 重复创建返回既有实体（200 + Idempotent-Replayed）。 */
  client_key?: string;
}

export const createWorkItem = (workspaceId: string, input: CreateWorkItemInput) =>
  apiFetch<WorkItem>(`/workspaces/${workspaceId}/work-items`, { method: 'POST', body: input });

/** 任务子树（先序遍历，含自身；树状列表/子任务面板的权威快照）。 */
export const getWorkItemTree = (workItemId: string) =>
  apiFetch<{ items: WorkItem[] }>(`/work-items/${workItemId}/tree`);

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

/** 打回重做：review/acceptance 退回 execution（M4 契约：reason 可选，落审计）。 */
export const returnWorkItem = (workItemId: string, reason: string | undefined, expectedVersion: number) =>
  apiFetch<WorkItem>(`/work-items/${workItemId}/commands/return`, {
    method: 'POST',
    body: { ...(reason ? { reason } : {}), expected_version: expectedVersion },
  });

// ── Plan（M1 编排：提交即同步执行；契约见 notes orchestration m1-plan-executor）──

export interface DispatchStepInput {
  verb: 'dispatch';
  agent_id: string;
  title: string;
  instruction: string;
  acceptance?: string[];
  priority?: Priority;
}

export interface DeferStepInput {
  verb: 'defer';
  reason: string;
  /** RFC3339；与「存在未静默子任务」至少居其一，否则 400（防死等）。 */
  wake_at?: string;
}

export interface FinishStepInput {
  verb: 'finish';
  summary: string;
}

export type PlanStepInput = DispatchStepInput | DeferStepInput | FinishStepInput;

export interface CreatePlanInput {
  work_item_id: string;
  agent_profile_id: string;
  source_run_id?: string;
  steps: PlanStepInput[];
}

/** 提交 plan（201 返回含 steps 执行结果；未知 verb / 非法 defer → 400 problem+json）。 */
export const createPlan = (workspaceId: string, input: CreatePlanInput) =>
  apiFetch<Plan>(`/workspaces/${workspaceId}/plans`, { method: 'POST', body: input });

export const getPlan = (planId: string) => apiFetch<Plan>(`/plans/${planId}`);

/** 主任务的最新一份 plan（按 created_at 最新，不限状态；无 plan → 404）。 */
export const getWorkItemPlan = (workItemId: string) => apiFetch<Plan>(`/work-items/${workItemId}/plan`);

// ── Run / Approval / Artifact ───────────────────────────────────────

export interface CreateRunInput {
  agent_profile_id?: string;
  output_contract?: 'languagegui/v1';
  runtime_preference?: { preferred: string; fallbacks?: string[] };
  requirements?: Record<string, string>;
  /** 实体级幂等键：同 key 重复创建返回既有 run（防队列 drain 重试重复建轮）。 */
  client_key?: string;
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

/** resolve 决议作用域：thread=本会话（work item）记住授权；workspace=整个工作区。 */
export const resolveApproval = (
  approvalId: string,
  runId: string,
  decision: 'approved' | 'rejected',
  reason?: string,
  scope: 'once' | 'thread' | 'workspace' = 'once',
) =>
  apiFetch<ApprovalRequest>(`/runs/${runId}/approvals/${approvalId}/commands/resolve`, {
    method: 'POST',
    body: { decision, reason: reason ?? '', scope },
  });

export const listArtifacts = (runId: string) =>
  apiFetch<{ items: Artifact[] }>(`/runs/${runId}/artifacts`);

/** 回放 Run 事件历史（对话页；只读投影，按 run_seq 排序）。 */
export const listRunEvents = (runId: string) =>
  apiFetch<{ items: RunEvent[] }>(`/runs/${runId}/events`);

export const listRunChanges = (runId: string) => apiFetch<RunChanges>(`/runs/${runId}/changes`);

export const getRunChangeDiff = (runId: string, path: string) =>
  apiFetch<RunChangeDiff>(`/runs/${runId}/changes/diff?${new URLSearchParams({ path }).toString()}`);

export const revertRunChanges = (runId: string, idempotencyKey: string) =>
  apiFetch<RunChanges>(`/runs/${runId}/commands/revert-changes`, {
    method: 'POST',
    body: { idempotency_key: idempotencyKey },
    idempotencyKey,
  });

/** 一个任务的全部 Run（对话轮次历史）。 */
export const listWorkItemRuns = (workItemId: string) =>
  apiFetch<{ items: ExecutionRun[] }>(`/work-items/${workItemId}/runs`);

/** 派发卡片列表（会话元模型 S1；一次发送 = 一个执行批次，新→旧）。 */
export const listWorkItemDispatches = (workItemId: string) =>
  apiFetch<{ items: DispatchCard[] }>(`/work-items/${workItemId}/dispatches`);

// ── 决策台账（会话元模型 S2）────────────────────────────────────────

/** 决策原话列表（创建时间升序；只读投影）。 */
export const listWorkItemDecisions = (workItemId: string) =>
  apiFetch<{ items: DecisionEntry[] }>(`/work-items/${workItemId}/decisions`);

export interface CreateDecisionInput {
  /** 用户原话（必填，trim 后非空；禁止转述进 quote）。 */
  quote: string;
  /** 钉出来源轮次；必须属于该 work item，否则 422。 */
  source_run_id?: string;
  /** 链回片段内位置（消息序号等）。 */
  source_ref?: string;
}

/** 钉为决策（201 回显）；幂等键照 createRun client_key 惯例由调用方按意图生成。 */
export const createDecision = (workItemId: string, input: CreateDecisionInput, idempotencyKey?: string) =>
  apiFetch<DecisionEntry>(`/work-items/${workItemId}/decisions`, {
    method: 'POST',
    body: input,
    ...(idempotencyKey ? { idempotencyKey } : {}),
  });

// ── FTS 检索（会话元模型 S4）────────────────────────────────────────

export interface WorkspaceSearchFilter {
  /** 检索词（空格分词 AND 语义）；空/纯符号服务端返回空 items。 */
  q: string;
  /** 可选，限定单任务。 */
  workItemId?: string;
  kind?: SearchKind;
  /** 服务端缺省 20、上限 100。 */
  limit?: number;
}

/** FTS 检索（片段摘要/决策台账/产物标题）；读命令，无幂等键。 */
export const searchWorkspace = (workspaceId: string, filter: WorkspaceSearchFilter) => {
  const params = new URLSearchParams();
  params.set('q', filter.q);
  if (filter.workItemId) params.set('work_item_id', filter.workItemId);
  if (filter.kind) params.set('kind', filter.kind);
  if (filter.limit != null) params.set('limit', String(filter.limit));
  return apiFetch<{ items: SearchItem[] }>(`/workspaces/${workspaceId}/search?${params.toString()}`);
};

// ── Agent 配置（agents/ 目录为真相源）──────────────────────────────

export interface PatchAgentInput {
  name?: string;
  role?: string;
  skills?: string[];
  instructions?: string;
  runtime_preference?: { preferred?: string; fallbacks?: string[]; mode?: 'default' | 'plan'; agent_preset?: string };
  model_override?: { ref?: string; provider?: string; model?: string; reasoning_effort?: string };
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

/** 凭据状态。写后不可读：服务端只回脱敏提示，永不返回完整 api_key 明文。 */
export interface ProviderCredentialStatus {
  configured: boolean;
  /** 末 4 位；密钥过短时缺省，以防外泄内容。 */
  masked_hint?: string;
  /** 密钥长度（字符数）。 */
  length?: number;
}

export const getProviderCredential = (providerId: string) =>
  apiFetch<ProviderCredentialStatus>(`/models/provider-credentials?provider_id=${encodeURIComponent(providerId)}`);

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
