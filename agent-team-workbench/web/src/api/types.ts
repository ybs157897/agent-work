/**
 * 与控制平面 DTO 对应的类型（协议文档 §5.1：snake_case）。
 * 字段以 internal/httpapi/dto.go 为准；Provider 原始字段不会出现在这里。
 */

export type AgentAvailability = 'enabled' | 'disabled';
export type AgentPresence = 'idle' | 'busy' | 'degraded' | 'offline';

export type WorkItemStatus = 'todo' | 'in_progress' | 'blocked' | 'completed' | 'cancelled';
export type WorkItemPhase = 'execution' | 'review' | 'acceptance' | '';
export type Priority = 'low' | 'medium' | 'high' | 'urgent';
/** WorkItem 的持久化记录类型；Chat 与 Task 共用执行内核但不共享记录入口。 */
export type WorkItemRecordKind = 'chat' | 'task';

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
  /** 系统内置 Agent（例如 Task Coordinator）；普通 Agent 配置页不得暴露或编辑。 */
  is_system?: boolean;
  /** 系统 Agent 类型；当前内置类型为 task_coordinator。 */
  kind?: 'task_coordinator' | string;
  /** 系统提示词是否允许修改；Coordinator 固定为 false。 */
  instructions_editable?: boolean;
  version: number;
}

/** POST /workspaces/{id}/agent-profiles；202 时外部配置仍待对账。 */
export interface CreateAgentResponse extends AgentProfile {
  config_sync_pending?: boolean;
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
  /** 参与线片段序号：同一 task_key 下第 N 段会话（轮换代际 +1）。 */
  segment_seq: number;
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
  /** 不可变记录类型；所有列表、导航与终态副作用均以此为硬边界。 */
  record_kind: WorkItemRecordKind;
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
  /** 任务台账滚动摘要（S2 确定性生成）：仅详情响应携带，列表/bootstrap 省略。 */
  rolling_digest?: string;
  version: number;
  created_at: string;
  updated_at: string;
}

// ── Native governance read model（WP6；服务端权威，前端不重算）──────────

export type GoalStatus = 'draft' | 'active' | 'waiting' | 'blocked' | 'completed' | 'cancelled';
export type TodoClass = 'advancement' | 'monitor' | 'user_gate' | 'blocker' | 'validation';
export type TodoStatus = 'pending' | 'claimed' | 'running' | 'waiting' | 'completed' | 'blocked' | 'cancelled';
export type GovernanceQuotaKind =
  | 'turn_count'
  | 'active_worker'
  | 'input_tokens_total'
  | 'input_uncached_tokens'
  | 'cache_read_tokens'
  | 'cache_write_tokens'
  | 'output_tokens'
  | 'cost_microusd';
export type GovernanceQuotaEnforcement = 'audit' | 'enforce';

export interface GovernanceQuotaPolicy {
  kind: GovernanceQuotaKind;
  limit: number;
  enforcement: GovernanceQuotaEnforcement;
}

export interface GovernanceEvidenceItem {
  source_kind: 'work_item' | 'plan' | 'run' | 'artifact' | 'approval' | 'delivery_brief' | 'validation_result';
  source_id: string;
  verification: 'observed' | 'passed' | 'failed' | 'accepted';
  summary: string;
  recorded_at: string;
}

export interface GovernanceDeliveryBriefSnapshot {
  id: string;
  schema_version: 'delivery-brief-snapshot/v1';
  goal_id: string;
  todo_id: string;
  work_item_id: string;
  snapshot_json: string;
  canonical_digest: string;
  as_of_event_seq: number;
  source_versions: Record<string, number>;
  freshness_state: 'current' | 'partial';
  created_at: string;
  client_key?: string;
}

export interface GovernanceGoal {
  id: string;
  workspace_id: string;
  root_work_item_id: string;
  objective: string;
  acceptance_contract: string[];
  status: GoalStatus;
  phase: string;
  current_todo_id: string | null;
  quota_policies: GovernanceQuotaPolicy[];
  completion_evidence_summary: GovernanceEvidenceItem[];
  version: number;
  created_at: string;
  updated_at: string;
}

export interface GovernanceDecisionScope {
  work_item_ids: string[];
  agent_ids: string[];
  runtime_capabilities: string[];
  write_scopes: string[];
  max_dispatch: number;
}

export interface GovernanceTodoClaim {
  owner_agent_id: string;
  version: number;
  claimed_at: string;
  expires_at: string;
}

export interface GovernanceTodo {
  id: string;
  goal_id: string;
  class: TodoClass;
  status: TodoStatus;
  instruction: string;
  acceptance: string[];
  resume_condition: string | null;
  priority: Priority;
  predecessors: string[];
  successors: string[];
  decision_scope: GovernanceDecisionScope;
  claim: GovernanceTodoClaim | null;
  claim_version: number;
  last_turn_seq: number;
  completion_turn_key: GovernanceTurnKey | null;
  completion_evidence_id: string | null;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface GovernanceTurnKey {
  goal_id: string;
  todo_id: string;
  turn_seq: number;
}

export interface GovernanceTurnReceiptHeader {
  turn_key: GovernanceTurnKey;
  attempt: number;
  schema_version: string;
  input_snapshot_digest: string;
  admission_client_key: string;
  source_run_id?: string;
  plan_client_key?: string;
  decision_digest?: string;
  canonical_digest: string;
  created_at: string;
}

export interface GovernanceTurnReceiptPhase {
  turn_key: GovernanceTurnKey;
  phase_seq: number;
  phase: string;
  payload: Record<string, unknown>;
  canonical_digest: string;
  plan_id?: string;
  run_ids?: string[];
  quota_reservation_keys?: string[];
  evidence?: GovernanceEvidenceItem[];
  created_at: string;
}

export interface GovernanceTurnReceipt {
  header: GovernanceTurnReceiptHeader;
  phases: GovernanceTurnReceiptPhase[];
}

export interface GovernanceQuotaReservation {
  goal_id: string;
  todo_id: string;
  turn_seq: number;
  quota_kind: GovernanceQuotaKind;
  status: 'reserved' | 'committed' | 'released' | 'expired';
  reserved_amount: number;
  committed_amount: number;
  released_amount: number;
  policy_limit: number;
  policy_enforcement: GovernanceQuotaEnforcement;
  policy_digest: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface GovernanceQuotaSpendEntry {
  goal_id: string;
  todo_id: string;
  turn_seq: number;
  quota_kind: Exclude<GovernanceQuotaKind, 'turn_count' | 'active_worker'>;
  run_id: string;
  amount: number;
  usage_basis: 'per_run';
  usage_digest: string;
  policy_digest: string;
  price_digest?: string;
  status: 'committed' | 'unresolved';
  reason?: string;
  created_at: string;
}

export interface GovernanceQuotaKindSummary {
  kind: GovernanceQuotaKind;
  committed: number;
  active_reserved: number;
  active_worker?: number;
  unresolved: GovernanceQuotaSpendEntry[];
}

export interface GovernanceGoalQuota {
  goal_id: string;
  policies: GovernanceQuotaPolicy[];
  kinds: GovernanceQuotaKindSummary[];
}

export interface GovernanceQuotaTurn {
  turn_key: GovernanceTurnKey;
  reservations: GovernanceQuotaReservation[];
  spend: GovernanceQuotaSpendEntry[];
}

export interface GovernanceQuotaSpendKey {
  turn_key: GovernanceTurnKey;
  quota_kind: Exclude<GovernanceQuotaKind, 'turn_count' | 'active_worker'>;
  run_id: string;
}

export interface GovernanceQuotaGapResolution {
  id: string;
  schema_version: 'quota-gap-resolution/v1';
  target: GovernanceQuotaSpendKey;
  original_usage_digest: string;
  original_policy_digest: string;
  original_price_digest?: string;
  status: 'reconciled';
  amount: number;
  evidence: GovernanceEvidenceItem;
  evidence_digest: string;
  canonical_digest: string;
  actor_kind: 'user';
  actor_id: string;
  reason: string;
  client_key?: string;
  created_at: string;
}

export interface GovernanceQuotaGapReconciliationInput {
  target: GovernanceQuotaSpendKey;
  amount: number;
  evidence: GovernanceEvidenceItem;
  actor_id: string;
  reason: string;
  client_key?: string;
}

export interface GovernanceGoalMetrics {
  goal_id: string;
  turn_count: number;
  run_count: number;
  input_tokens_total: number;
  input_uncached_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  output_tokens: number;
  cost_microusd: number;
}

export interface GovernanceMetrics {
  workspace_id: string;
  source_event_seq: number;
  plan_decode_success: number;
  plan_decode_errors: Record<string, number>;
  repair_attempts: number;
  repair_successes: number;
  repair_blockers: number;
  receipt_replays: number;
  receipt_conflicts: number;
  projection_divergences: number;
  evidence_finish_rejections: number;
  user_unblocks: number;
  projection_updates: number;
  handoffs: number;
  evidence_items: number;
  goal_summaries: GovernanceGoalMetrics[];
}

export type GovernanceActorKind = 'agent' | 'runtime';
export type GovernanceHandoffStatus = 'pending' | 'accepted' | 'transferred' | 'rejected' | 'cancelled';
export type GovernanceClaimTransferState = 'retained_by_source' | 'claimed_by_target' | 'transferred';

export interface GovernanceActorRef {
  kind: GovernanceActorKind;
  id: string;
}

export interface GovernanceHandoff {
  id: string;
  goal_id: string;
  todo_id: string;
  source: GovernanceActorRef;
  target: GovernanceActorRef;
  reason: string;
  context_summary: string;
  evidence: GovernanceEvidenceItem[];
  open_risks: string[];
  acceptance?: string;
  status: GovernanceHandoffStatus;
  claim_transfer_state: GovernanceClaimTransferState;
  source_claim_version: number;
  target_claim_version: number;
  actor: GovernanceActorRef;
  client_key?: string;
  accepted_by?: GovernanceActorRef;
  accepted_at?: string;
  version: number;
  created_at: string;
  updated_at: string;
}

export interface GovernanceProjectionCursor {
  event_stream_seq: number;
  through_turn_seq: number;
}

export interface GovernanceGoalProjection {
  goal_id: string;
  goal_progress: Record<string, unknown>;
  todo_current_state: Record<string, unknown>;
  receipt_timeline: Record<string, unknown>[];
  evidence_summary: GovernanceEvidenceItem[];
  next_action_checkpoint: Record<string, unknown>;
  counters: Record<string, number>;
  source_cursor: GovernanceProjectionCursor;
  digest: string;
  version: number;
  updated_at: string;
}

export type GovernanceProjectionRepairStatus = 'pending' | 'running' | 'completed' | 'failed';

export interface GovernanceProjectionRepair {
  id: string;
  goal_id: string;
  status: GovernanceProjectionRepairStatus;
  scope: ('goal_progress' | 'todo_current_state' | 'receipt_timeline' | 'evidence_summary' | 'next_action_checkpoint')[];
  source_cursor: GovernanceProjectionCursor;
  replayed_event_count: number;
  replayed_receipt_count: number;
  error_code?: string;
  error_message?: string;
  client_key?: string;
  version: number;
  started_at: string;
  completed_at?: string;
  created_at: string;
  updated_at: string;
}

// ── Task Coordinator（任务控制线）──────────────────────────────────

export type CoordinatorRuntime = 'mock' | 'codex_local' | 'kimi_local';
export type CoordinatorStatus =
  | 'queued'
  | 'running'
  | 'waiting_retry'
  | 'waiting_user'
  | 'blocked'
  | 'completed'
  | 'cancelled';
export type CoordinatorStage =
  | 'plan'
  | 'dispatch'
  | 'attempt'
  | 'failure'
  | 'retry'
  | 'reassign'
  | 'settlement'
  | 'result'
  | 'acceptance';

export interface CoordinatorAgentRef {
  id: string;
  name: string;
  role?: string;
  runtime?: CoordinatorRuntime;
}

export interface CoordinatorFailure {
  code?: string;
  message: string;
  retryable?: boolean;
}

/** 一次 Worker 尝试；attempt 序列由控制面落盘，前端只读投影。 */
export interface CoordinatorAttempt {
  id: string;
  step_id?: string;
  run_id?: string;
  agent?: CoordinatorAgentRef;
  status: string;
  attempt: number;
  max_attempts?: number;
  retry_of?: string;
  started_at: string;
  finished_at?: string;
  failure?: CoordinatorFailure;
  next_action?: string;
}

/** Task 级因果时间线；正文仍由 run/AgentOutput 投影渲染。 */
export interface CoordinatorTimelineEntry {
  id: string;
  stage: CoordinatorStage;
  kind?: string;
  title: string;
  message?: string;
  status?: string;
  occurred_at: string;
  agent?: CoordinatorAgentRef;
  attempt?: number;
  run_id?: string;
  retry_of?: string;
  failure?: CoordinatorFailure;
}

export interface CoordinatorSnapshot {
  root_work_item_id: string;
  work_item_id: string;
  status: CoordinatorStatus;
  stage?: CoordinatorStage;
  progress?: number | null;
  completed_steps?: number;
  total_steps?: number;
  current_agent?: CoordinatorAgentRef;
  next_action?: string;
  next_action_at?: string;
  blocker?: Blocker;
  runtime_label?: CoordinatorRuntime;
  model_ref?: string;
  reasoning_effort?: string;
  prompt_version?: string;
  prompt_locked?: boolean;
  attempts: CoordinatorAttempt[];
  timeline: CoordinatorTimelineEntry[];
  updated_at: string;
  version: number;
}

/** Workspace 级 Coordinator 配置；instructions 永远不在此 DTO 中出现。 */
export interface CoordinatorConfig {
  workspace_id: string;
  kind: 'task_coordinator';
  /** Persisted control-plane names. */
  runtime_label: CoordinatorRuntime;
  model_ref: string;
  reasoning_effort: string;
  fallback_runtime_label?: CoordinatorRuntime | '';
  fallback_model_ref?: string;
  prompt_version: string;
  prompt_locked: boolean;
  instructions_editable: false;
  version: number;
}

export interface PatchCoordinatorConfigInput {
  runtime_label: CoordinatorRuntime;
  model_ref: string;
  reasoning_effort: string;
  fallback_runtime_label?: CoordinatorRuntime | '';
  fallback_model_ref?: string;
  expected_version: number;
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

// ── Dispatch（会话元模型 S1：一次发送形成的执行批次）──────────────────

export type DispatchTrigger = 'user_message' | 'lead_plan' | 'wakeup';
export type DispatchStatus = 'running' | 'collecting' | 'completed' | 'degraded' | 'cancelled';

/** 派发成员 run 摘要（会话组；服务端按 created_at 升序）。 */
export interface DispatchRun {
  id: string;
  work_item_id: string;
  agent_profile_id?: string;
  agent_name?: string;
  status: RunStatus;
  /** 一行摘要（S1 为 run 指令摘录，确定性生成）。 */
  summary?: string;
}

/** 触发消息摘录（锚回来源 run）。 */
export interface DispatchTriggerMessage {
  run_id?: string;
  excerpt: string;
}

/** GET /work-items/{id}/dispatches 的卡片（新→旧）。 */
export interface DispatchCard {
  id: string;
  work_item_id: string;
  trigger: DispatchTrigger;
  /** 接诊 run / plan source run；@直达批次省略。 */
  lead_run_id?: string;
  status: DispatchStatus;
  trigger_message?: DispatchTriggerMessage;
  runs: DispatchRun[];
  created_at: string;
  closed_at?: string;
}

// ── 任务评论（任务控制面 RFC §4.9：append-only，revision 在根 Task 维度分配）──

export type TaskCommentKind = 'note' | 'requirement' | 'review_feedback';
export type TaskCommentActorKind = 'user' | 'system' | 'runtime';

/** GET/POST /work-items/{id}/comments 的评论行（无 update/delete）。 */
export interface TaskComment {
  id: string;
  workspace_id: string;
  root_work_item_id: string;
  /** 评论落点的子 Task；根 Task 评论时等于 root。 */
  work_item_id: string;
  revision: number;
  kind: TaskCommentKind;
  body: string;
  actor_kind: TaskCommentActorKind;
  actor_id: string;
  source_run_id?: string | null;
  source_ref?: string | null;
  client_key?: string | null;
  created_at: string;
}

/** 评论列表（revision 正序分页；cursor 与 Workspace SSE stream_seq 严格分离）。 */
export interface TaskCommentList {
  items: TaskComment[];
  /** 下一页游标（revision > 已返回最大值）；没有更多时缺省/null。 */
  next_revision?: number | null;
  latest_revision: number;
}

/** POST comments 只接受 note|requirement；review_feedback 只能由 return 命令生成。 */
export interface TaskCommentCreateInput {
  kind: Exclude<TaskCommentKind, 'review_feedback'>;
  body: string;
  /** 可选；必须属于该 Task 树（422 comment_source_run_mismatch）。 */
  source_run_id?: string;
  source_ref?: string;
  /** 实体级幂等键：唯一域 (root_work_item_id, client_key)；同 key 不同 body 409。 */
  client_key?: string;
  /** note 可省略；requirement 推荐必填，防止基于终态/旧 phase 追加。 */
  expected_work_item_version?: number;
}

// ── 决策台账（会话元模型 S2：任务级共享记忆的用户原话引文）────────────

/** GET/POST /work-items/{id}/decisions 的台账行。 */
export interface DecisionEntry {
  id: string;
  work_item_id: string;
  /** 用户原话（引文保真；服务端只 trim，不改写）。 */
  quote: string;
  /** 钉出来源轮次；缺省时未钉来源。 */
  source_run_id?: string;
  /** 链回片段内位置（消息序号等）。 */
  source_ref?: string;
  created_at: string;
}

// ── Review Queue（任务控制面 RFC §4.10：服务端权威 read model）─────────

/** 各来源读模型版本水位（同一 read snapshot 内读取）。 */
export interface SourceWatermark {
  as_of_event_seq: number;
  work_item_version?: number;
  coordinator_version?: number;
  latest_run_version?: number;
  comment_revision?: number;
}

/** 队列行：work_item + pending 元数据 + Coordinator 状态摘要。 */
export interface ReviewQueueItem {
  work_item: WorkItem;
  /** = phase_entered_at；review→execution→review 必须得到新时间。 */
  pending_since: string;
  coordinator?: {
    status: string;
    stage?: string;
    updated_at: string;
    version: number;
  } | null;
  latest_run_id?: string | null;
  source_watermark: SourceWatermark;
}

/** GET /workspaces/{id}/review-queue 响应；total_count 是 badge 唯一权威值。 */
export interface ReviewQueueResponse {
  items: ReviewQueueItem[];
  total_count: number;
  next_cursor: string | null;
  generated_at: string;
}

// ── Delivery Brief（任务控制面 RFC §4.11：确定性服务端聚合，无 LLM 摘要）──

export interface DeliveryBriefConclusion {
  coordinator_status: string;
  stage?: string;
  summary?: string;
  next_action?: string;
  version: number;
}

/** 一次尝试的服务端摘要（attempts 上限 50，按 attempt number 排序）。 */
export interface AttemptSummary {
  attempt: number;
  role: 'coordinator' | 'worker' | 'evaluation';
  run_id: string;
  agent_id?: string;
  agent_name?: string;
  status: string;
  started_at?: string | null;
  finished_at?: string | null;
  retry_of?: string | null;
  failure?: { code?: string; message: string; retryable?: boolean } | null;
}

export type EvidenceStatus = 'passed' | 'failed' | 'warning' | 'unknown';
export type EvidenceTrust = 'control_plane' | 'runtime_reported' | 'model_reported';

/** 证据条目（evidence 上限 200；普通 Markdown 只算 model_reported）。 */
export interface EvidenceItem {
  id: string;
  source_kind: 'coordinator_event' | 'run_status' | 'evaluation_verdict' | 'tool_result' | 'artifact' | 'change_set';
  source_id: string;
  label?: string;
  status: EvidenceStatus;
  trust: EvidenceTrust;
  occurred_at: string;
}

export interface RunEvidence {
  run: ExecutionRun;
  summary?: string;
  evidence: EvidenceItem[];
  truncated: boolean;
}

/** 按 Run 分组的文件变更（不把多个 Run 混成一个 diff）。 */
export interface BriefChangeSet {
  run_id: string;
  files: { path: string; added: number; deleted: number; status: 'added' | 'modified' | 'deleted' | 'renamed' }[];
  total_files: number;
  total_added: number;
  total_deleted: number;
  truncated: boolean;
}

export interface RiskItem {
  source_kind: string;
  source_id: string;
  code: string;
  message: string;
  severity: 'low' | 'medium' | 'high';
}

export interface DeliveryBriefFreshness {
  generated_at: string;
  /** 聚合 read snapshot 内的 Workspace MAX(stream_seq)；与前端 eventCursor 比较。 */
  as_of_event_seq: number;
  source_versions?: Record<string, number>;
  state: 'current' | 'partial';
  missing_sources?: string[];
}

export interface DeliveryBriefTruncation {
  attempts: boolean;
  runs: boolean;
  files: boolean;
  artifacts: boolean;
  comments: boolean;
}

export interface DeliveryBrief {
  work_item: WorkItem;
  acceptance_criteria: string[];
  conclusion: DeliveryBriefConclusion;
  attempts: AttemptSummary[];
  runs: RunEvidence[];
  changes: BriefChangeSet | null;
  artifacts: Artifact[];
  blocker: { code: string; message: string; source: string; run_id?: string | null; created_at: string } | null;
  risks: RiskItem[];
  /** requirement/review_feedback 评论（按 root revision 排序，上限 200）。 */
  comments: TaskComment[];
  freshness: DeliveryBriefFreshness;
  truncation: DeliveryBriefTruncation;
}

// ── FTS 检索（会话元模型 S4：索引为派生存储，纯请求-响应，无 SSE）────

export type SearchKind = 'segment_summary' | 'decision' | 'artifact';

/** GET /workspaces/{id}/search 的命中项。 */
export interface SearchItem {
  kind: SearchKind;
  /** 命中归属任务（decision/artifact 也定位到其 work item）。 */
  work_item_id: string;
  /** 决策 id / work item id（segment_summary）/ artifact id。 */
  source_id: string;
  /** 任务标题 / quote 前 80 字 / logical_path。 */
  title: string;
  /** 正文命中摘录：[] 包裹命中词、… 截断省略。 */
  snippet: string;
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

// ── Run Journal（运行环节时间线；只读投影，排障视图）────────────────

/** 七段相位标识：dispatch → spawn → handshake → first_event → streaming → settle → post。 */
export type RunJournalPhaseName =
  | 'dispatch'
  | 'spawn'
  | 'handshake'
  | 'first_event'
  | 'streaming'
  | 'settle'
  | 'post';

/** 相位结果：ok=正常、failed=失败、skipped=跳过；null=未闭合（进行中或中断）。 */
export type RunJournalOutcome = 'ok' | 'failed' | 'skipped';

/** 相位失败证据（journal 专属：比 RunFailure 多 family 归因族）。 */
export interface RunJournalFailure {
  code: string;
  message: string;
  family: string;
  retryable: boolean;
}

/** 单个相位区段（post 相位每个钩子各一对，detail.hook 携带钩子名）。 */
export interface RunJournalPhase {
  phase: RunJournalPhaseName;
  attempt: number;
  entered_at: string;
  /** null = 未闭合：run 仍在该相位（进行中），或进程中断未落到终态。 */
  closed_at: string | null;
  outcome: RunJournalOutcome | null;
  duration_ms: number | null;
  failure: RunJournalFailure | null;
  detail: Record<string, unknown> | null;
}

export interface RunJournal {
  run_id: string;
  generated_at: string;
  phases: RunJournalPhase[];
  /** 进程输出摘要：chunks 条、truncated 表示服务端只保留了尾部。 */
  log: { chunks: number; truncated: boolean };
  /** 治理回合归属；chat run 等无治理归属时为 null。 */
  governance: { goal_id: string; todo_id: string; turn_seq: number; digest: string } | null;
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
  // dispatch 域（会话元模型 S1）；updated 的生产者随 S3 状态收口接入，先行进白名单。
  'dispatch.created',
  'dispatch.updated',
  // 决策台账（会话元模型 S2）：用户原话钉为决策时发布。
  'decision.created',
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
  'file_changes.reverted',
  'approval.requested',
  'approval.resolved',
  'approval.expired',
  'artifact.created',
  'artifact.updated',
  'usage.updated',
  'runtime.health_changed',
  'run.recovery_started',
  'run.recovery_completed',
  'run.recovery_failed',
  // Task Coordinator 控制线；缺少 record_kind 的事件由前端丢弃。
  'coordinator.queued',
  'coordinator.started',
  'coordinator.message_received',
  'coordinator.plan_updated',
  'coordinator.attempt_updated',
  'coordinator.retry_scheduled',
  'coordinator.recovery_started',
  'coordinator.state_changed',
  'coordinator.blocked',
  'coordinator.completed',
  // Native governance intent/receipt events；只触发服务端 read model 失效重取。
  'goal.created',
  'goal.state_changed',
  'todo.created',
  'todo.state_changed',
  'todo.claim_changed',
  'turn.receipt_appended',
  'handoff.created',
  'handoff.state_changed',
  'goal.evidence_added',
  'validation.result_recorded',
  'projection.updated',
  'projection.repair_state_changed',
  'quota.reservation_changed',
  'quota.spend_recorded',
  'quota.gap_reconciled',
  'delivery_brief.snapshot_created',
  // 任务控制面补全（RFC §10）：Location/context/comment 写入与流同事务提交。
  'execution_host.updated',
  'workspace_location.created',
  'workspace_location.updated',
  'workspace_location.unavailable',
  'work_item.development_context_updated',
  'task_comment.created',
] as const;

/**
 * internal 事件名单（Run Journal，与 domain/events.go internalEventNames 一致）。
 * 这些事件只落 run_events、从不经 SSE 推送，因此不注册 EventSource listener；
 * 读取走 /runs/{id}/journal 调试 API。事件契约测试按
 * EVENT_NAMES ∪ INTERNAL_EVENT_NAMES == AsyncAPI enum 双向对账。
 */
export const INTERNAL_EVENT_NAMES = [
  'run.phase_entered',
  'run.phase_closed',
  'run.log_chunk',
  'run.decision',
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
