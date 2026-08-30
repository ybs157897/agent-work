/**
 * 与控制平面 DTO 对应的类型（协议文档 §5.1：snake_case）。
 * 字段以 internal/httpapi/dto.go 为准；Provider 原始字段不会出现在这里。
 */

export type AgentAvailability = 'enabled' | 'disabled';
export type AgentPresence = 'idle' | 'busy' | 'degraded' | 'offline';

export type WorkItemStatus = 'todo' | 'in_progress' | 'blocked' | 'completed' | 'cancelled';
export type WorkItemPhase = 'execution' | 'review' | 'acceptance' | '';
export type Priority = 'low' | 'medium' | 'high' | 'urgent';

export type RunStatus =
  | 'queued'
  | 'starting'
  | 'running'
  | 'waiting_approval'
  | 'interrupting'
  | 'cancelling'
  | 'reconnecting'
  | 'succeeding'
  | 'succeeded'
  | 'interrupted'
  | 'cancelled'
  | 'lost'
  | 'failed';

export type ApprovalStatus = 'pending' | 'approved' | 'rejected' | 'expired';

export interface Workspace {
  id: string;
  name: string;
  timezone: string;
  version: number;
}

export interface AgentProfile {
  id: string;
  slug?: string;
  name: string;
  role: string;
  skills: string[];
  instructions?: string;
  availability: AgentAvailability;
  presence: AgentPresence;
  avatar?: string;
  runtime_preference?: { preferred?: string; fallbacks?: string[]; mode?: 'default' | 'plan'; agent_preset?: string };
  model_override?: { ref?: string; provider?: string; model?: string; reasoning_effort?: string };
  policy?: AgentPolicy;
  version: number;
}

/** 跨 Run 会话锚点（GET /agent-profiles/{id}/task-sessions；墓碑行服务端已过滤）。 */
export interface TaskSession {
  id: string;
  agent_profile_id: string;
  adapter_id: string;
  task_key: string;
  session_ref?: string;
  session_params?: Record<string, unknown>;
  display_id?: string;
  runs_count: number;
  input_tokens_cum: number;
  created_at: string;
  updated_at: string;
}

/** POST /agent-profiles/{id}/commands/wake 的 202 响应。 */
export interface WakeResult {
  wakeup_id: string;
  status: string;
  wake_at: string;
}

/** Agent 权限配置：控制平面统一语义，由各 Runtime adapter 映射为原生参数。 */
export interface AgentPolicy {
  tools?: string[];
  approval_policy?: 'auto' | 'approve_high_risk' | 'manual' | '';
  sandbox?: string;
  permission_preset?: string;
}

export interface PermissionPresetEntry {
  id: string;
  name: string;
  description?: string;
}

export interface AgentPresetEntry {
  id: string;
  trust: string;
  is_default?: boolean;
  name?: string;
  description?: string;
  broken?: string;
  order?: number;
}

export interface DSHCatalog {
  agent_presets: AgentPresetEntry[];
  permission_presets: PermissionPresetEntry[];
}

export interface Blocker {
  code: string;
  message: string;
  source: string;
  created_at: string;
}

export interface WorkItem {
  id: string;
  workspace_id: string;
  title: string;
  description: string;
  status: WorkItemStatus;
  phase?: WorkItemPhase;
  priority: Priority;
  due_date: string | null;
  /** 父任务 id（M1 编排 dispatch 建子任务；omitempty，根任务缺省）。 */
  parent_id?: string;
  agent_profile_id?: string;
  /** 执行锁属主 run id（F1；非空=任务正被该 run 执行，防双跑）。 */
  locked_by_run_id?: string;
  /** 获取执行锁的时间（F1；无锁时缺省）。 */
  locked_at?: string;
  blocker?: Blocker;
  runs_count: number;
  latest_run_id?: string;
  version: number;
  created_at: string;
  updated_at: string;
}

// ── Plan（M1 编排：lead agent 提交的有序动作批次）────────────────────

export type PlanStatus = 'active' | 'waiting' | 'finished' | 'cancelled' | 'failed';
export type PlanStepStatus = 'pending' | 'executed' | 'skipped' | 'failed';
/** M1 词汇表子集；未知 verb 提交即 400（consult_knowledge/join 归后续里程碑）。 */
export type PlanVerb = 'dispatch' | 'defer' | 'finish';

/** PlanDTO step：payload 为提交时的 JSON 原文（按 verb 判别）。 */
export interface PlanStep {
  seq: number;
  verb: PlanVerb;
  status: PlanStepStatus;
  payload: Record<string, unknown>;
  /** dispatch 步执行后建出的子任务 / 首 run；缺省表示尚未产出。 */
  result_work_item_id?: string | null;
  result_run_id?: string | null;
  error?: string | null;
}

export interface Plan {
  id: string;
  workspace_id: string;
  work_item_id: string;
  agent_profile_id: string;
  source_run_id?: string | null;
  status: PlanStatus;
  /** 本 plan 被同主任务的新 waiting→新提交 supersede 时指向新 plan id。 */
  superseded_by?: string | null;
  steps: PlanStep[];
  version: number;
  created_at: string;
  updated_at: string;
}

export interface RunFailure {
  code: string;
  message: string;
  retryable: boolean;
}

export interface ExecutionRun {
  id: string;
  work_item_id: string;
  agent_profile_id?: string;
  status: RunStatus;
  runtime_label?: string;
  progress?: number | null;
  retry_of?: string;
  failure?: RunFailure;
  version: number;
  created_at: string;
  updated_at: string;
  /** Run 维度累计用量投影（usage_basis 说明口径）；未上报时缺省，UI 防御式不渲染。 */
  usage_in?: number;
  usage_out?: number;
  usage_cached?: number;
  usage_basis?: string;
}

export interface ApprovalRequest {
  id: string;
  run_id: string;
  work_item_id: string;
  kind: string;
  risk: string;
  status: ApprovalStatus;
  summary: string;
  resolved_by?: string;
  resolved_at?: string;
}

export interface Artifact {
  id: string;
  run_id: string;
  logical_path: string;
  mime: string;
  size: number;
  sha256: string;
  status: 'draft' | 'accepted';
  created_at: string;
}

export interface Activity {
  id: string;
  kind: string;
  message: string;
  occurred_at: string;
  work_item_id?: string;
}

export interface BoardCounts {
  todo: number;
  in_progress: number;
  blocked: number;
  completed: number;
}

export interface Dashboard {
  active_agents: number;
  running_tasks: number;
  completed_today: number;
  board_counts: BoardCounts;
  recent_activities: Activity[];
}

export interface Health {
  control_plane: string;
  runners: unknown[];
}

/** RuntimeBinding：凭据只含引用，绝不明文（协议 §10.1）。 */
export interface RuntimeBinding {
  id: string;
  runtime_label: string;
  adapter_id: string;
  adapter_version: string;
  provider_version?: string;
  provider: string;
  model: string;
  credential_ref?: string;
  capabilities: Record<string, string>;
  status: string;
  version: number;
}

export interface ProbeResult {
  runtime_binding_id: string;
  ok: boolean;
  provider_version?: string;
  capabilities: Record<string, string>;
  error: string | null;
}

export interface Bootstrap {
  workspace: Workspace;
  dashboard: Dashboard;
  agents: { items: AgentProfile[] };
  work_items: { items: WorkItem[] };
  health: Health;
  event_cursor: number;
}

export interface Me {
  user_id: string;
  name: string;
  role: string;
  feature_flags: Record<string, boolean>;
}

/** application/problem+json（协议文档 §5.5） */
export interface Problem {
  type: string;
  title: string;
  status: number;
  code?: string;
  detail?: string;
  instance?: string;
  request_id?: string;
  retryable?: boolean;
  current_version?: number;
}

/** Run 事件历史（GET /runs/{id}/events；按 run_seq 排序的只读投影） */
export interface RunEvent {
  run_seq: number;
  event_type: string;
  payload?: Record<string, unknown>;
  occurred_at: string;
  agent_id?: string;
}

/** 文件变更投影：供回复尾部的 Codex 风格变更卡使用。 */
export interface RunChange {
  path: string;
  additions: number;
  deletions: number;
  write_count: number;
  last_turn_index: number;
  kind: 'modified' | 'added' | 'deleted' | 'renamed';
  binary: boolean;
  can_revert?: boolean;
  revert_reason?: string;
}

export interface RunChanges {
  file_count: number;
  additions: number;
  deletions: number;
  files: RunChange[];
  state: 'ready' | 'reverted' | 'unavailable';
  can_revert: boolean;
  reason?: string;
  version: number;
}

export interface RunChangeDiff {
  path: string;
  diff: string;
  truncated?: boolean;
  binary?: boolean;
}

/** SSE Canonical Event Envelope（contracts/events/asyncapi.yaml） */
export interface CanonicalEvent {
  contract_version: string;
  event_id: string;
  workspace_id: string;
  stream_seq: number;
  aggregate: { type: string; id: string; version: number };
  run_seq?: number;
  type: string;
  occurred_at: string;
  actor?: { kind: 'user' | 'runtime' | 'system'; id: string };
  correlation_id?: string;
  data?: Record<string, unknown>;
  agent_id?: string;
}

/** SSE event name 白名单（与 domain/events.go 一致） */
export const EVENT_NAMES = [
  'dashboard.metrics.updated',
  'activity.appended',
  'system.health_changed',
  'agent_profile.created',
  'agent_profile.updated',
  'agent_availability.changed',
  'agent_presence.updated',
  'work_item.created',
  'work_item.updated',
  'work_item.moved',
  'work_item.assigned',
  'work_item.blocked',
  'work_item.unblocked',
  'work_item.completed',
  'work_item.locked',
  'work_item.lock_preempted',
  'plan.submitted',
  'plan.step_executed',
  'plan.waiting',
  'plan.finished',
  'plan.failed',
  'session.decision',
  'session.compacted',
  'run.created',
  'run.started',
  'run.status_changed',
  'run.progress_updated',
  'run.plan_updated',
  'run.completed',
  'run.failed',
  'run.cancelled',
  'run.lost',
  'message.delta',
  'message.completed',
  'tool.started',
  'tool.progress',
  'tool.completed',
  'tool.failed',
  'subagent.updated',
  'approval.requested',
  'approval.resolved',
  'approval.expired',
  'artifact.created',
  'artifact.updated',
  'usage.updated',
  'runtime.health_changed',
  'runner.connected',
  'runner.disconnected',
  'run.recovery_started',
  'run.recovery_completed',
  'run.recovery_failed',
] as const;

/** 模型注册表条目（models/ 目录为真相源；词汇对齐 pi-ai provider profile）。 */
export interface ModelEntry {
  id: string;
  display_name: string;
  /** UI 展示分类；与实际 provider 路由相互独立。 */
  category?: string;
  /** 供应商稳定 id，凭据与分组按此匹配。 */
  provider_id: string;
  /** DSH / pi-ai 路由名，可与 label 不同。 */
  provider: string;
  api?: 'openai-completions' | 'openai-responses' | 'anthropic-messages' | '';
  model: string;
  api_key_env?: string;
  base_url?: string;
  context_window?: number;
  max_tokens?: number;
  notes?: string;
}
