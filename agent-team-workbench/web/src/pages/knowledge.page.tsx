import {
  AlertCircle,
  BookOpen,
  Bot,
  CheckCircle2,
  ChevronRight,
  CircleHelp,
  FileText,
  GitBranch,
  Inbox,
  Network,
  RefreshCw,
  Search,
  Send,
  Settings2,
  ShieldCheck,
  XCircle,
} from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError } from '../api/client';
import {
  cancelKnowledgeJob,
  createKnowledgeInquiry,
  curateKnowledgeSubmission,
  getKnowledgeConfig,
  getKnowledgeItem,
  getKnowledgeJob,
  getKnowledgeVersion,
  listKnowledgeItems,
  listKnowledgeJobs,
  listKnowledgeRelations,
  listKnowledgeSubmissions,
  listKnowledgeVersions,
  patchKnowledgeConfig,
  publishKnowledgeSubmission,
  repealKnowledgeItem,
  submitKnowledgeCandidate,
  type KnowledgeConfig,
  type KnowledgeChange,
  type KnowledgeCoverage,
  type KnowledgeCoverageEntry,
  type KnowledgeItem,
  type KnowledgeItemDetails,
  type KnowledgeJob,
  type KnowledgeJobStatus,
  type KnowledgeRelation,
  type KnowledgeSource,
  type KnowledgeSourceKind,
  type KnowledgeSubmission,
  type KnowledgeVersion,
} from '../api/knowledge';
import type { AgentProfile } from '../api/types';
import { AgentOutput } from '../components/chat/agent-output';
import { Drawer } from '../components/drawer';
import { Button, Card, EmptyState, Field, Input, Select, Skeleton, StatusPill, Textarea, cx } from '../components/ui';
import { Toggle } from '../components/toggle';
import { useAgentsStore } from '../stores/agents.store';
import { toast } from '../stores/toast.store';
import { useWorkspaceStore } from '../stores/workspace.store';
import { captureScope, isCurrent } from '../stores/scope';
import { isUserManagedAgent } from '../utils/agent-scope';
import { formatDateTime } from '../utils/format';

type AsyncState<T> =
  | { kind: 'loading' }
  | { kind: 'ready'; value: T }
  | { kind: 'error'; message: string };

type KnowledgeListValue<T> = { items: T[]; next_cursor?: string | null; truncated?: boolean };
type KnowledgeListState<T> = AsyncState<KnowledgeListValue<T>>;

const STATUS_LABELS: Record<string, string> = {
  candidate: '候选',
  draft: '草稿',
  effective: '已发布/有效知识',
  superseded: '已取代',
  repealed: '已废止',
  complete: '完整',
  partial: '部分覆盖',
  missing: '存在缺口',
  received: '已收件',
  processing: '整理中',
  accepted: '已发布',
  merged: '已合并',
  rejected: '已拒绝',
  needs_review: '待复核',
  queued: '排队中',
  running: '调查中',
  waiting_retry: '等待重试',
  completed: '已完成',
  incomplete: '信息不全',
  conflict: '存在冲突',
  cancelled: '已取消',
  failed: '失败',
};

const STATUS_CLASSES: Record<string, string> = {
  effective: 'border-status-success/30 bg-status-success/10 text-status-success',
  completed: 'border-status-success/30 bg-status-success/10 text-status-success',
  accepted: 'border-status-success/30 bg-status-success/10 text-status-success',
  running: 'border-status-info/30 bg-status-info/10 text-status-info',
  processing: 'border-status-info/30 bg-status-info/10 text-status-info',
  queued: 'border-border-strong bg-surface-sunken text-text-secondary',
  received: 'border-border-strong bg-surface-sunken text-text-secondary',
  candidate: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  draft: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  needs_review: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  incomplete: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  waiting_retry: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  conflict: 'border-status-error/35 bg-status-error/10 text-status-error',
  failed: 'border-status-error/35 bg-status-error/10 text-status-error',
  rejected: 'border-status-error/35 bg-status-error/10 text-status-error',
  cancelled: 'border-border-subtle bg-surface-sunken text-text-tertiary',
  superseded: 'border-border-subtle bg-surface-sunken text-text-tertiary',
  repealed: 'border-border-subtle bg-surface-sunken text-text-tertiary',
  merged: 'border-status-info/30 bg-status-info/10 text-status-info',
  complete: 'border-status-success/30 bg-status-success/10 text-status-success',
  partial: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
  missing: 'border-status-warning/35 bg-status-warning/10 text-status-warning',
};

const RELATION_LABELS: Record<string, string> = {
  related_to: '相关于',
  depends_on: '依赖',
  impacts: '影响',
  triggers: '触发',
  calls: '调用',
  subscribes_to: '订阅',
  shares_state: '共享状态',
  constrained_by: '受约束于',
  conflicts_with: '冲突',
  supersedes: '取代',
};

const KNOWLEDGE_KIND_LABELS: Record<string, string> = {
  fact: '事实',
  rule: '规则',
  decision: '决策',
  observation: '观察',
};

const SOURCE_KIND_LABELS: Record<string, string> = {
  run: '运行记录',
  artifact: '产物',
  work_item: '任务',
  document: '文档',
  code: '代码',
  test: '测试',
  user: '用户',
  agent: 'Agent',
};

const TERMINAL_JOB_STATUSES = new Set<KnowledgeJobStatus>([
  'completed',
  'incomplete',
  'conflict',
  'cancelled',
  'failed',
]);

function statusLabel(status: string | undefined): string {
  return status ? STATUS_LABELS[status] ?? status : '未标记';
}

export function knowledgeStatusClass(status: string | undefined): string {
  return STATUS_CLASSES[status ?? ''] ?? 'border-border-subtle bg-surface-raised text-text-secondary';
}

export function relationLabel(kind: string): string {
  return RELATION_LABELS[kind] ?? kind;
}

export function knowledgeKindLabel(kind: string): string {
  return KNOWLEDGE_KIND_LABELS[kind] ?? kind;
}

export function sourceKindLabel(kind: string): string {
  return SOURCE_KIND_LABELS[kind] ?? kind;
}

export function knowledgeSourceKindFromRef(ref: string): KnowledgeSourceKind {
  const normalized = ref.trim();
  if (normalized.startsWith('run_')) return 'run';
  if (normalized.startsWith('artifact_')) return 'artifact';
  return 'document';
}

export function buildKnowledgeCandidateChange(input: {
  title: string;
  body: string;
  kind: string;
  visibility: 'private' | 'workspace';
  evidenceRef: string;
  evidenceExcerpt: string;
}): KnowledgeChange {
  const evidenceRef = input.evidenceRef.trim();
  return {
    base_version: 0,
    title: input.title,
    body: input.body,
    kind: input.kind,
    visibility: input.visibility,
    sources: [{
      kind: knowledgeSourceKindFromRef(evidenceRef),
      ref: evidenceRef,
      excerpt: input.evidenceExcerpt,
    }],
  };
}

export function canPublishKnowledgeSubmission(submission: KnowledgeSubmission): boolean {
  const resultItemIDs = submission.result_item_ids ?? [];
  const resultVersionIDs = submission.result_version_ids ?? [];
  return submission.status === 'needs_review'
    && resultVersionIDs.length > 0
    && resultVersionIDs.length === resultItemIDs.length;
}

export function formatKnowledgeScope(scope: Record<string, unknown> | undefined): string {
  if (!scope || Object.keys(scope).length === 0) return '未限定适用范围';
  return Object.entries(scope)
    .map(([key, value]) => key + '=' + String(value))
    .join(' · ');
}

export function isKnowledgeScopeFilter(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true;
  if (trimmed.startsWith('{')) {
    try {
      const parsed: unknown = JSON.parse(trimmed);
      return parsed !== null && typeof parsed === 'object' && !Array.isArray(parsed);
    } catch {
      return false;
    }
  }
  const separator = trimmed.indexOf('=');
  return separator > 0 && trimmed.slice(separator + 1).trim().length > 0;
}

export function isKnowledgeJobTerminal(status: KnowledgeJobStatus): boolean {
  return TERMINAL_JOB_STATUSES.has(status);
}

export function knowledgeJobNeedsSubmissionRefresh(job: Pick<KnowledgeJob, 'mode' | 'status'>): boolean {
  return job.mode === 'curation' && isKnowledgeJobTerminal(job.status);
}

export function jobPollNeedsSubmissionRefresh(previousStatus: KnowledgeJobStatus, nextStatus: KnowledgeJobStatus): boolean {
  return !isKnowledgeJobTerminal(previousStatus) && isKnowledgeJobTerminal(nextStatus);
}

function errorMessage(error: unknown, fallback: string): string {
  if (error instanceof ApiError) return error.message || fallback;
  if (error instanceof Error) return error.message || fallback;
  return fallback;
}

function configAgentId(config: KnowledgeConfig): string {
  return config.librarian_agent_id;
}

function upsertById<T extends { id: string }>(items: T[], next: T): T[] {
  return items.some((item) => item.id === next.id)
    ? items.map((item) => (item.id === next.id ? next : item))
    : [next, ...items];
}

function jobCoverage(job: KnowledgeJob): KnowledgeCoverage {
  const candidate = job.result?.coverage;
  if (Array.isArray(candidate)) {
    return { ...job.coverage, entries: candidate };
  }
  return candidate ?? job.coverage;
}

export default function KnowledgePage() {
  const workspace = useWorkspaceStore((state) => state.workspace);
  const workspaceId = workspace?.id ?? null;
  const navigate = useNavigate();
  const agents = useAgentsStore((state) => state.agents).filter(isUserManagedAgent);
  const [configState, setConfigState] = useState<AsyncState<KnowledgeConfig>>({ kind: 'loading' });
  const [itemsState, setItemsState] = useState<KnowledgeListState<KnowledgeItem>>({ kind: 'loading' });
  const [submissionsState, setSubmissionsState] = useState<KnowledgeListState<KnowledgeSubmission>>({ kind: 'loading' });
  const [jobsState, setJobsState] = useState<KnowledgeListState<KnowledgeJob>>({ kind: 'loading' });
  const [scopeDraft, setScopeDraft] = useState('');
  const [scopeFilter, setScopeFilter] = useState('');
  const [scopeError, setScopeError] = useState<string | undefined>();
  const [viewAgentId, setViewAgentId] = useState('');
  const [selectedItem, setSelectedItem] = useState<KnowledgeItem | null>(null);
  const [question, setQuestion] = useState('');
  const [taskPurpose, setTaskPurpose] = useState('');
  const [inquiryError, setInquiryError] = useState<string | undefined>();
  const [inquirySubmitting, setInquirySubmitting] = useState(false);
  const [candidateSourceAgent, setCandidateSourceAgent] = useState('');
  const [candidateTitle, setCandidateTitle] = useState('');
  const [candidateKind, setCandidateKind] = useState('fact');
  const [candidateVisibility, setCandidateVisibility] = useState<'private' | 'workspace'>('workspace');
  const [candidateBody, setCandidateBody] = useState('');
  const [candidateEvidence, setCandidateEvidence] = useState('');
  const [candidateEvidenceExcerpt, setCandidateEvidenceExcerpt] = useState('');
  const [candidateError, setCandidateError] = useState<string | undefined>();
  const [candidateSubmitting, setCandidateSubmitting] = useState(false);
  const [selectedJobId, setSelectedJobId] = useState<string | null>(null);
  const [actionId, setActionId] = useState<string | null>(null);
  const [configAction, setConfigAction] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [itemsLoadingMore, setItemsLoadingMore] = useState(false);
  const [itemsLoadMoreError, setItemsLoadMoreError] = useState<string | undefined>();
  const configRequest = useRef(0);
  const itemsRequest = useRef(0);
  const submissionsRequest = useRef(0);
  const jobsRequest = useRef(0);

  const loadConfig = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++configRequest.current;
    setConfigState({ kind: 'loading' });
    try {
      const value = await getKnowledgeConfig(workspaceId);
      if (request !== configRequest.current || !isCurrent(scope)) return;
      setConfigState({ kind: 'ready', value });
    } catch (error) {
      if (request !== configRequest.current || !isCurrent(scope)) return;
      setConfigState({
        kind: 'error',
        message: error instanceof ApiError && error.status === 404
          ? '还没有配置知识管理员 Agent'
          : errorMessage(error, '知识管理员配置加载失败'),
      });
    }
  }, [workspaceId]);

  const loadItems = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++itemsRequest.current;
    setItemsLoadingMore(false);
    setItemsLoadMoreError(undefined);
    setItemsState({ kind: 'loading' });
    try {
      const value = await listKnowledgeItems(workspaceId, {
        status: 'effective',
        scope: scopeFilter || undefined,
        agent_id: viewAgentId || undefined,
        limit: 200,
      });
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState({ kind: 'ready', value });
      setSelectedItem((previous) => {
        if (!previous) return null;
        return value.items.some((item) => item.id === previous.id) || previous.status === 'repealed' ? previous : null;
      });
    } catch (error) {
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState({ kind: 'error', message: errorMessage(error, '已发布/有效知识加载失败') });
    }
  }, [scopeFilter, viewAgentId, workspaceId]);

  const loadMoreItems = useCallback(async () => {
    if (!workspaceId || itemsLoadingMore || itemsState.kind !== 'ready') return;
    const cursor = itemsState.value.next_cursor;
    if (!cursor) return;
    const scope = captureScope();
    const request = ++itemsRequest.current;
    setItemsLoadingMore(true);
    setItemsLoadMoreError(undefined);
    try {
      const value = await listKnowledgeItems(workspaceId, {
        status: 'effective',
        scope: scopeFilter || undefined,
        agent_id: viewAgentId || undefined,
        cursor,
        limit: 200,
      });
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsState((state) => {
        if (state.kind !== 'ready') return state;
        const existing = new Set(state.value.items.map((item) => item.id));
        const appended = value.items.filter((item) => !existing.has(item.id));
        return {
          kind: 'ready',
          value: {
            items: [...state.value.items, ...appended],
            next_cursor: value.next_cursor ?? null,
          },
        };
      });
    } catch (error) {
      if (request !== itemsRequest.current || !isCurrent(scope)) return;
      setItemsLoadMoreError(errorMessage(error, '更多知识加载失败'));
    } finally {
      if (request === itemsRequest.current && isCurrent(scope)) setItemsLoadingMore(false);
    }
  }, [itemsLoadingMore, itemsState, scopeFilter, viewAgentId, workspaceId]);

  const loadSubmissions = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++submissionsRequest.current;
    setSubmissionsState({ kind: 'loading' });
    try {
      const value = await listKnowledgeSubmissions(workspaceId, { agent_id: viewAgentId || undefined });
      if (request !== submissionsRequest.current || !isCurrent(scope)) return;
      setSubmissionsState({ kind: 'ready', value });
    } catch (error) {
      if (request !== submissionsRequest.current || !isCurrent(scope)) return;
      setSubmissionsState({ kind: 'error', message: errorMessage(error, '候选收件箱加载失败') });
    }
  }, [viewAgentId, workspaceId]);

  const loadJobs = useCallback(async () => {
    if (!workspaceId) return;
    const scope = captureScope();
    const request = ++jobsRequest.current;
    setJobsState({ kind: 'loading' });
    try {
      const value = await listKnowledgeJobs(workspaceId, { agent_id: viewAgentId || undefined });
      if (request !== jobsRequest.current || !isCurrent(scope)) return;
      setJobsState({ kind: 'ready', value });
      setSelectedJobId((previous) => previous && value.items.some((job) => job.id === previous) ? previous : value.items[0]?.id ?? null);
      if (value.items.some(knowledgeJobNeedsSubmissionRefresh)) void loadSubmissions();
    } catch (error) {
      if (request !== jobsRequest.current || !isCurrent(scope)) return;
      setJobsState({ kind: 'error', message: errorMessage(error, '知识作业加载失败') });
    }
  }, [loadSubmissions, viewAgentId, workspaceId]);

  useEffect(() => {
    setConfigState({ kind: 'loading' });
    setItemsState({ kind: 'loading' });
    setSubmissionsState({ kind: 'loading' });
    setJobsState({ kind: 'loading' });
    setViewAgentId('');
    setScopeDraft('');
    setScopeFilter('');
    setScopeError(undefined);
    setItemsLoadingMore(false);
    setItemsLoadMoreError(undefined);
    setSelectedItem(null);
    setSelectedJobId(null);
    setCandidateSourceAgent('');
    setCandidateTitle('');
    setCandidateBody('');
    setCandidateEvidence('');
    setCandidateEvidenceExcerpt('');
    setCandidateError(undefined);
    setQuestion('');
    setTaskPurpose('');
    setInquiryError(undefined);
    setInquirySubmitting(false);
    setCandidateSubmitting(false);
    setActionId(null);
    setConfigAction(null);
    setRefreshing(false);
    if (!workspaceId) {
      return;
    }
    void loadConfig();
  }, [loadConfig, workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;
    void loadSubmissions();
    void loadJobs();
  }, [loadJobs, loadSubmissions, viewAgentId, workspaceId]);

  useEffect(() => {
    if (!workspaceId) return;
    // A scope or private-view switch invalidates the open detail as well as
    // the list. This keeps an older item's body from surviving a new view
    // while its fenced request is still in flight.
    setSelectedItem(null);
    void loadItems();
  }, [loadItems, workspaceId]);

  const selectedJob = useMemo(
    () => jobsState.kind === 'ready' ? jobsState.value.items.find((job) => job.id === selectedJobId) ?? null : null,
    [jobsState, selectedJobId],
  );

  useEffect(() => {
    if (!workspaceId || !selectedJobId || !selectedJob || isKnowledgeJobTerminal(selectedJob.status)) return;
    let cancelled = false;
    const scope = captureScope();
    let timer: number | undefined;
    const poll = async () => {
      try {
        const value = await getKnowledgeJob(workspaceId, selectedJobId);
        if (cancelled || !isCurrent(scope)) return;
        setJobsState((state) => state.kind === 'ready'
          ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, value) } }
          : state);
        if (selectedJob.mode === 'curation' && jobPollNeedsSubmissionRefresh(selectedJob.status, value.status)) {
          void loadSubmissions();
        }
        if (!isKnowledgeJobTerminal(value.status)) timer = window.setTimeout(poll, 1500);
      } catch (error) {
        if (!cancelled && isCurrent(scope)) toast.error(errorMessage(error, '知识作业进度读取失败'));
      }
    };
    timer = window.setTimeout(poll, 800);
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [loadSubmissions, selectedJob, selectedJobId, workspaceId]);

  const refreshAll = async () => {
    const scope = captureScope();
    setRefreshing(true);
    try {
      await Promise.all([loadConfig(), loadItems(), loadSubmissions(), loadJobs()]);
    } finally {
      if (isCurrent(scope)) setRefreshing(false);
    }
  };

  const saveAdminAgent = async (agentId: string) => {
    if (configState.kind !== 'ready' || !workspaceId) return;
    const config = configState.value;
    const scope = captureScope();
    setConfigAction('admin');
    try {
      const next = await patchKnowledgeConfig(workspaceId, {
        librarian_agent_id: agentId || null,
        expected_version: config.version,
      });
      if (!isCurrent(scope)) return;
      setConfigState({ kind: 'ready', value: next });
      toast.info('知识管理员配置已保存');
    } catch (error) {
      if (isCurrent(scope)) {
        toast.error(errorMessage(error, '知识管理员配置保存失败'));
        await loadConfig();
      }
    } finally {
      if (isCurrent(scope)) setConfigAction(null);
    }
  };

  const saveAgentSetting = async (agentId: string, field: 'enabled' | 'auto_collect', checked: boolean) => {
    if (configState.kind !== 'ready' || !workspaceId) return;
    const config = configState.value;
    if (agentId !== config.librarian_agent_id) return;
    const scope = captureScope();
    setConfigAction(field);
    try {
      const next = await patchKnowledgeConfig(workspaceId, {
        librarian_agent_id: config.librarian_agent_id || null,
        enabled: field === 'enabled' ? checked : config.enabled,
        auto_collect: field === 'auto_collect' ? checked : config.auto_collect,
        expected_version: config.version,
      });
      if (!isCurrent(scope)) return;
      setConfigState({ kind: 'ready', value: next });
      toast.info('知识管理员开关已保存');
    } catch (error) {
      if (isCurrent(scope)) {
        toast.error(errorMessage(error, 'Agent 知识配置保存失败'));
        await loadConfig();
      }
    } finally {
      if (isCurrent(scope)) setConfigAction(null);
    }
  };

  const handleInquiry = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const trimmedQuestion = question.trim();
    if (!workspaceId || !trimmedQuestion || inquirySubmitting) return;
    const scope = captureScope();
    setInquirySubmitting(true);
    setInquiryError(undefined);
    try {
      const job = await createKnowledgeInquiry(
        workspaceId,
        {
          question: trimmedQuestion,
          context: taskPurpose.trim() || undefined,
        },
        viewAgentId || undefined,
      );
      if (!isCurrent(scope)) return;
      setJobsState((state) => state.kind === 'ready'
        ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, job) } }
        : { kind: 'ready', value: { items: [job] } });
      setSelectedJobId(job.id);
      setQuestion('');
      setTaskPurpose('');
      toast.info('知识调查作业已创建');
    } catch (error) {
      if (!isCurrent(scope)) return;
      const message = errorMessage(error, '知识调查作业创建失败');
      setInquiryError(message);
      toast.error(message);
    } finally {
      if (isCurrent(scope)) setInquirySubmitting(false);
    }
  };

  const handleCandidateSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!workspaceId || candidateSubmitting) return;
    const sourceAgent = candidateSourceAgent.trim();
    const title = candidateTitle.trim();
    const body = candidateBody.trim();
    const evidenceRef = candidateEvidence.trim();
    const evidenceExcerpt = candidateEvidenceExcerpt.trim();
    if (!sourceAgent || !title || !body || !evidenceRef || !evidenceExcerpt) {
      setCandidateError('请填写来源 Agent、标题、Markdown 正文、证据引用和证据摘录');
      return;
    }
    const scope = captureScope();
    setCandidateSubmitting(true);
    setCandidateError(undefined);
    const clientKey = crypto.randomUUID();
    try {
      const submission = await submitKnowledgeCandidate(workspaceId, {
        agent_id: sourceAgent,
        client_key: clientKey,
        changes: [{
          ...buildKnowledgeCandidateChange({
            title,
            body,
            kind: candidateKind.trim() || 'fact',
            visibility: candidateVisibility,
            evidenceRef,
            evidenceExcerpt,
          }),
        }],
      });
      if (!isCurrent(scope)) return;
      setSubmissionsState((state) => state.kind === 'ready'
        ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, submission) } }
        : { kind: 'ready', value: { items: [submission] } });
      setCandidateTitle('');
      setCandidateBody('');
      setCandidateEvidence('');
      setCandidateEvidenceExcerpt('');
      toast.info('候选知识已提交到收件箱');
    } catch (error) {
      if (isCurrent(scope)) {
        const message = errorMessage(error, '候选知识提交失败');
        setCandidateError(message);
        toast.error(message);
      }
    } finally {
      if (isCurrent(scope)) setCandidateSubmitting(false);
    }
  };

  const handleSelectJob = async (jobId: string) => {
    setSelectedJobId(jobId);
    if (!workspaceId) return;
    const scope = captureScope();
    try {
      const value = await getKnowledgeJob(workspaceId, jobId);
      if (!isCurrent(scope)) return;
      setJobsState((state) => state.kind === 'ready'
        ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, value) } }
        : state);
      if (knowledgeJobNeedsSubmissionRefresh(value)) void loadSubmissions();
    } catch (error) {
      if (isCurrent(scope)) toast.error(errorMessage(error, '知识作业详情读取失败'));
    }
  };

  const handleCancelJob = async (job: KnowledgeJob) => {
    if (!workspaceId || actionId || isKnowledgeJobTerminal(job.status)) return;
    const scope = captureScope();
    setActionId(job.id);
    try {
      const next = await cancelKnowledgeJob(workspaceId, job.id);
      if (!isCurrent(scope)) return;
      setJobsState((state) => state.kind === 'ready'
        ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, next) } }
        : state);
      toast.info('知识调查已请求取消');
    } catch (error) {
      if (isCurrent(scope)) toast.error(errorMessage(error, '取消知识调查失败'));
    } finally {
      if (isCurrent(scope)) setActionId(null);
    }
  };

  const handleSubmissionAction = async (submission: KnowledgeSubmission, action: 'curate' | 'publish') => {
    if (!workspaceId || actionId) return;
    const scope = captureScope();
    setActionId(submission.id + ':' + action);
    try {
      if (action === 'curate') {
        const job = await curateKnowledgeSubmission(workspaceId, submission.id);
        if (!isCurrent(scope)) return;
        setJobsState((state) => state.kind === 'ready'
          ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, job) } }
          : { kind: 'ready', value: { items: [job] } });
        setSelectedJobId(job.id);
        void loadSubmissions();
        toast.info('整理作业已启动');
      } else {
        const next = await publishKnowledgeSubmission(workspaceId, submission.id);
        if (!isCurrent(scope)) return;
        setSubmissionsState((state) => state.kind === 'ready'
          ? { kind: 'ready', value: { ...state.value, items: upsertById(state.value.items, next) } }
          : state);
        void loadItems();
        toast.info('发布请求已提交');
      }
    } catch (error) {
      if (isCurrent(scope)) toast.error(errorMessage(error, action === 'curate' ? '启动整理失败' : '发布候选失败'));
    } finally {
      if (isCurrent(scope)) setActionId(null);
    }
  };

  if (!workspaceId) {
    return (
      <main className="page-shell">
        <header className="page-header">
          <div>
            <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">知识治理</p>
            <h1 className="page-title">知识管理员</h1>
          </div>
        </header>
        <Card padded>
          <EmptyState
            icon={<BookOpen className="h-5 w-5" aria-hidden="true" />}
            title="等待工作区"
            description="工作区准备完成后，这里会显示共享知识和知识管理员作业"
          />
        </Card>
      </main>
    );
  }

  return (
    <main className="page-shell">
      <header className="page-header">
        <div className="min-w-0">
          <p className="text-caption font-medium uppercase tracking-wider text-brand-primary">知识治理</p>
          <h1 className="page-title">知识管理员</h1>
          <p className="page-subtitle mt-1">
            调查、核实并组织工作区知识；每个结论都保留来源、版本和适用范围。
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-tight">
          <StatusPill title="当前工作区">
            <BookOpen className="h-3.5 w-3.5 text-brand-primary" aria-hidden="true" />
            <span className="max-w-44 truncate">{workspace?.name ?? workspaceId}</span>
          </StatusPill>
          <Button type="button" onClick={() => void refreshAll()} disabled={refreshing}>
            <RefreshCw className={cx('h-4 w-4', refreshing && 'animate-spin')} aria-hidden="true" />
            刷新
          </Button>
        </div>
      </header>

      <div className="grid min-w-0 gap-comfortable xl:grid-cols-[minmax(0,1.08fr)_minmax(22rem,0.92fr)]">
        <AdminConfigCard
          state={configState}
          agents={agents}
          action={configAction}
          onOpenAgents={() => navigate('/agents')}
          onSelectAdmin={(agentId) => void saveAdminAgent(agentId)}
          onToggle={(field, checked) => void saveAgentSetting(configState.kind === 'ready' ? configState.value.librarian_agent_id : '', field, checked)}
        />
        <InquiryCard
          question={question}
          purpose={taskPurpose}
          error={inquiryError}
          submitting={inquirySubmitting}
          onQuestionChange={setQuestion}
          onPurposeChange={setTaskPurpose}
          onSubmit={handleInquiry}
        />
      </div>

      <PublishedKnowledgeSection
        state={itemsState}
        agents={agents}
        scopeDraft={scopeDraft}
        scopeFilter={scopeFilter}
        scopeError={scopeError}
        loadingMore={itemsLoadingMore}
        loadMoreError={itemsLoadMoreError}
        viewAgentId={viewAgentId}
        onScopeDraftChange={(value) => {
          setScopeDraft(value);
          setScopeError(undefined);
        }}
        onApplyScope={() => {
          const next = scopeDraft.trim();
          if (!isKnowledgeScopeFilter(next)) {
            setScopeError('适用范围筛选仅支持 key=value 或 JSON 对象，例如 project=atlas 或 {"project":"atlas"}');
            return;
          }
          setScopeError(undefined);
          setScopeFilter(next);
        }}
        onViewAgentChange={setViewAgentId}
        onRetry={() => void loadItems()}
        onLoadMore={() => void loadMoreItems()}
        onOpen={setSelectedItem}
      />

      <div className="grid min-w-0 gap-comfortable xl:grid-cols-[minmax(0,0.92fr)_minmax(0,1.08fr)]">
        <ManualCandidateCard
          agents={agents}
          sourceAgent={candidateSourceAgent}
          title={candidateTitle}
          kind={candidateKind}
          visibility={candidateVisibility}
          body={candidateBody}
          evidence={candidateEvidence}
          evidenceExcerpt={candidateEvidenceExcerpt}
          error={candidateError}
          submitting={candidateSubmitting}
          onSourceAgentChange={setCandidateSourceAgent}
          onTitleChange={setCandidateTitle}
          onKindChange={setCandidateKind}
          onVisibilityChange={setCandidateVisibility}
          onBodyChange={setCandidateBody}
          onEvidenceChange={setCandidateEvidence}
          onEvidenceExcerptChange={setCandidateEvidenceExcerpt}
          onSubmit={handleCandidateSubmit}
        />
        <SubmissionInbox
          state={submissionsState}
          actionId={actionId}
          onRetry={() => void loadSubmissions()}
          onAction={handleSubmissionAction}
        />
      </div>
      <KnowledgeJobsSection
        state={jobsState}
        selectedJobId={selectedJobId}
        actionId={actionId}
        onRetry={() => void loadJobs()}
        onSelect={handleSelectJob}
        onCancel={handleCancelJob}
      />

      {selectedItem && (
        <KnowledgeDetailDrawer
          workspaceId={workspaceId}
          item={selectedItem}
          agentId={viewAgentId || undefined}
          onClose={() => setSelectedItem(null)}
          onRepealed={(next) => {
            setSelectedItem(next);
            setItemsState((state) => state.kind === 'ready'
              ? { kind: 'ready', value: { ...state.value, items: state.value.items.filter((candidate) => candidate.id !== next.id) } }
              : state);
          }}
        />
      )}
    </main>
  );
}

function AdminConfigCard({
  state,
  agents,
  action,
  onOpenAgents,
  onSelectAdmin,
  onToggle,
}: {
  state: AsyncState<KnowledgeConfig>;
  agents: AgentProfile[];
  action: string | null;
  onOpenAgents: () => void;
  onSelectAdmin: (agentId: string) => void;
  onToggle: (field: 'enabled' | 'auto_collect', checked: boolean) => void;
}) {
  if (state.kind === 'loading') {
    return (
      <Card padded aria-label="知识管理员配置加载中">
        <div className="space-y-snug">
          <Skeleton className="h-5 w-44" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      </Card>
    );
  }

  if (state.kind === 'error') {
    return (
      <Card padded className="border-status-warning/35">
        <div className="flex items-start gap-snug">
          <CircleHelp className="mt-0.5 h-5 w-5 shrink-0 text-status-warning" aria-hidden="true" />
          <div className="min-w-0">
            <h2 className="text-h3 text-text-primary">管理员配置</h2>
            <p className="mt-micro text-body text-text-secondary">{state.message}</p>
          <p className="mt-tight text-caption text-text-tertiary">共享知识仍可浏览；先配置一个已有 Agent 才能启动调查和自动收集并整理。</p>
            <Button type="button" variant="primary" size="sm" className="mt-snug" onClick={onOpenAgents}>
              配置已有 Agent
            </Button>
          </div>
        </div>
      </Card>
    );
  }

  const config = state.value;
  const selectedAdmin = configAgentId(config);
  const selectedAgent = agents.find((agent) => agent.id === selectedAdmin);
  const enabledBusy = action === 'enabled';
  const autoCollectBusy = action === 'auto_collect';

  return (
    <Card padded>
      <div className="flex flex-wrap items-start justify-between gap-base">
        <div className="min-w-0">
          <div className="flex items-center gap-tight">
            <Settings2 className="h-5 w-5 text-brand-primary" aria-hidden="true" />
            <h2 className="text-h3 text-text-primary">管理员配置</h2>
          </div>
          <p className="mt-micro text-caption text-text-secondary">选择一个已有 Agent 承担知识调查；是否启用管理员与是否自动收集并整理分开控制。</p>
        </div>
        <StatusPill className={config.enabled === false ? knowledgeStatusClass('cancelled') : knowledgeStatusClass('effective')}>
          {config.enabled === false ? '未启用' : '配置可用'}
        </StatusPill>
      </div>

      <Field
        label="知识管理员 Agent"
        hint={selectedAdmin ? '调查作业会沿用该 Agent 的运行配置。' : '尚未选择；只展示已有 Agent。'}
        className="mt-comfortable"
      >
        <Select
          value={selectedAdmin}
          onChange={(event) => onSelectAdmin(event.target.value)}
          disabled={action === 'admin' || agents.length === 0}
          aria-label="选择知识管理员 Agent"
        >
          <option value="">未选择</option>
          {agents.map((agent) => (
            <option key={agent.id} value={agent.id}>
              {agent.name} · {agent.role}
            </option>
          ))}
        </Select>
      </Field>

      {agents.length === 0 ? (
        <div className="mt-comfortable rounded-card border border-dashed border-border-strong px-snug py-base text-caption text-text-secondary">
          暂无已配置 Agent。前往智能体配置页创建或检查已有配置。
        </div>
      ) : (
        <div className="mt-comfortable space-y-snug">
          <div className="flex min-w-0 items-center gap-snug rounded-card border border-border-subtle bg-surface-base/55 px-snug py-snug">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-button bg-brand-muted text-brand-primary" aria-hidden="true">
              <Bot className="h-4 w-4" />
            </div>
            <div className="min-w-0 flex-1">
              <p className="truncate text-body font-medium text-text-primary">{(selectedAgent?.name ?? selectedAdmin) || '尚未选择管理员'}</p>
              <p className="truncate text-caption text-text-tertiary">
                {selectedAgent ? selectedAgent.role + ' · ' + (selectedAgent.model_override?.ref || '沿用 Agent 当前模型') : '先从上方选择已有 Agent'}
              </p>
            </div>
            {selectedAgent ? <StatusPill className="border-brand-primary/30 bg-brand-muted text-brand-primary">已选择</StatusPill> : null}
          </div>

          <div className="grid gap-tight sm:grid-cols-2" aria-label="知识管理员开关">
            <div className="flex min-h-12 items-center justify-between gap-snug rounded-card border border-border-subtle bg-surface-base/55 px-snug py-tight">
              <span>
                <span className="block text-body font-medium text-text-primary">启用管理员</span>
                <span className="block text-caption text-text-tertiary">允许创建调查和整理作业</span>
              </span>
              <Toggle
                checked={config.enabled}
                onChange={(checked) => onToggle('enabled', checked)}
                disabled={!selectedAgent || enabledBusy || autoCollectBusy}
                ariaLabel="启用知识管理员"
              />
            </div>
            <div className="flex min-h-12 items-center justify-between gap-snug rounded-card border border-border-subtle bg-surface-base/55 px-snug py-tight">
              <span>
                <span className="block text-body font-medium text-text-primary">自动收集并整理</span>
              <span className="block text-caption text-text-tertiary">任务收尾后收集并整理候选变化</span>
              </span>
              <Toggle
                checked={config.auto_collect}
                onChange={(checked) => onToggle('auto_collect', checked)}
                disabled={!selectedAgent || enabledBusy || autoCollectBusy}
                ariaLabel="自动收集并整理知识候选"
              />
            </div>
          </div>
          {!selectedAgent ? <p className="text-caption text-status-warning">没有选中的 Agent 时，开关保持关闭且不可修改。</p> : null}
        </div>
      )}
    </Card>
  );
}

function InquiryCard({
  question,
  purpose,
  error,
  submitting,
  onQuestionChange,
  onPurposeChange,
  onSubmit,
}: {
  question: string;
  purpose: string;
  error?: string;
  submitting: boolean;
  onQuestionChange: (value: string) => void;
  onPurposeChange: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <Card padded>
      <div className="flex items-center gap-tight">
        <Search className="h-5 w-5 text-brand-primary" aria-hidden="true" />
        <h2 className="text-h3 text-text-primary">发起知识调查</h2>
      </div>
      <p className="mt-micro text-caption text-text-secondary">用自然语言说明问题和用途；管理员会记录调查范围、证据和覆盖缺口。</p>
      <form className="mt-comfortable space-y-snug" onSubmit={onSubmit}>
        <Field
          label="调查问题"
          hint="例如：功能 A 涉及哪些功能 B？请列出依赖、约束和来源。"
          error={error}
        >
          <Textarea
            value={question}
            onChange={(event) => onQuestionChange(event.target.value)}
            placeholder="输入要查清的问题"
            rows={4}
            required
            invalid={Boolean(error)}
          />
        </Field>
        <Field label="任务用途" hint="帮助管理员判断需要的完整性和交付形式。">
          <Input
            value={purpose}
            onChange={(event) => onPurposeChange(event.target.value)}
            placeholder="例如：给开发 Agent 规划实现范围"
          />
        </Field>
        <div className="flex items-center justify-between gap-snug">
          <p className="max-w-sm text-caption text-text-tertiary">提交后会创建真实知识作业；进度、冲突和缺口会在下方保留。</p>
          <Button type="submit" variant="primary" disabled={submitting || !question.trim()}>
            <Send className="h-4 w-4" aria-hidden="true" />
            {submitting ? '创建中…' : '开始调查'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function ManualCandidateCard({
  agents,
  sourceAgent,
  title,
  kind,
  visibility,
  body,
  evidence,
  evidenceExcerpt,
  error,
  submitting,
  onSourceAgentChange,
  onTitleChange,
  onKindChange,
  onVisibilityChange,
  onBodyChange,
  onEvidenceChange,
  onEvidenceExcerptChange,
  onSubmit,
}: {
  agents: AgentProfile[];
  sourceAgent: string;
  title: string;
  kind: string;
  visibility: 'private' | 'workspace';
  body: string;
  evidence: string;
  evidenceExcerpt: string;
  error?: string;
  submitting: boolean;
  onSourceAgentChange: (value: string) => void;
  onTitleChange: (value: string) => void;
  onKindChange: (value: string) => void;
  onVisibilityChange: (value: 'private' | 'workspace') => void;
  onBodyChange: (value: string) => void;
  onEvidenceChange: (value: string) => void;
  onEvidenceExcerptChange: (value: string) => void;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
}) {
  return (
    <Card padded>
      <div className="flex items-center gap-tight">
        <FileText className="h-5 w-5 text-brand-primary" aria-hidden="true" />
        <h2 className="text-h3 text-text-primary">手动提交候选</h2>
      </div>
      <p className="mt-micro text-caption text-text-secondary">选择实际来源 Agent，写入一条可整理的候选；正文支持 Markdown。</p>
      <form className="mt-comfortable space-y-snug" onSubmit={onSubmit}>
        <div className="grid gap-snug sm:grid-cols-2">
          <Field label="来源 Agent" hint="必须选择，不从当前登录身份猜测。">
            <Select
              value={sourceAgent}
              onChange={(event) => onSourceAgentChange(event.target.value)}
              required
              invalid={Boolean(error && !sourceAgent)}
              aria-label="选择候选来源 Agent"
            >
              <option value="">选择 Agent</option>
              {agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name} · {agent.role}</option>)}
            </Select>
          </Field>
          <Field label="知识类型" hint="事实、规则或决策">
            <Select value={kind} onChange={(event) => onKindChange(event.target.value)} aria-label="选择知识类型" required>
              <option value="fact">事实</option>
              <option value="rule">规则</option>
              <option value="decision">决策</option>
            </Select>
          </Field>
        </div>
        <Field label="标题">
          <Input value={title} onChange={(event) => onTitleChange(event.target.value)} placeholder="给候选知识一个可引用的标题" required invalid={Boolean(error && !title)} />
        </Field>
        <div className="grid gap-snug sm:grid-cols-2">
          <Field label="可见性" hint="私有候选只对来源 Agent 和有权限的管理员可见。">
            <Select value={visibility} onChange={(event) => onVisibilityChange(event.target.value as 'private' | 'workspace')} aria-label="选择候选可见性">
              <option value="workspace">工作区共享</option>
              <option value="private">来源 Agent 私有</option>
            </Select>
          </Field>
          <Field label="证据引用" hint="填写运行记录、文档、测试或产物的可定位引用。">
            <Input value={evidence} onChange={(event) => onEvidenceChange(event.target.value)} placeholder="run_… / docs/…#L…" required invalid={Boolean(error && !evidence)} />
          </Field>
        </div>
        <Field label="证据摘录" hint="必须粘贴来源中的原始文字；它独立于候选正文，用于管理员核实。">
          <Textarea
            value={evidenceExcerpt}
            onChange={(event) => onEvidenceExcerptChange(event.target.value)}
            placeholder="粘贴能直接支持这条知识的原文片段"
            rows={4}
            required
            invalid={Boolean(error && !evidenceExcerpt)}
          />
        </Field>
        <Field label="Markdown 正文">
          <Textarea
            value={body}
            onChange={(event) => onBodyChange(event.target.value)}
            placeholder="记录事实、条件、边界和证据上下文"
            rows={7}
            required
            invalid={Boolean(error && !body)}
          />
        </Field>
        {error ? <p role="alert" className="text-caption text-status-error">{error}</p> : null}
        <div className="flex items-center justify-between gap-snug">
          <p className="max-w-sm text-caption text-text-tertiary">提交后进入候选收件箱，整理和发布仍由管理员与领域 owner 决定。</p>
          <Button type="submit" variant="primary" disabled={submitting || agents.length === 0}>
            <Send className="h-4 w-4" aria-hidden="true" />
            {submitting ? '提交中…' : '提交候选'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function PublishedKnowledgeSection({
  state,
  agents,
  scopeDraft,
  scopeFilter,
  scopeError,
  loadingMore,
  loadMoreError,
  viewAgentId,
  onScopeDraftChange,
  onApplyScope,
  onViewAgentChange,
  onRetry,
  onLoadMore,
  onOpen,
}: {
  state: KnowledgeListState<KnowledgeItem>;
  agents: AgentProfile[];
  scopeDraft: string;
  scopeFilter: string;
  scopeError?: string;
  loadingMore: boolean;
  loadMoreError?: string;
  viewAgentId: string;
  onScopeDraftChange: (value: string) => void;
  onApplyScope: () => void;
  onViewAgentChange: (value: string) => void;
  onRetry: () => void;
  onLoadMore: () => void;
  onOpen: (item: KnowledgeItem) => void;
}) {
  return (
    <section className="ink-paper-panel overflow-hidden rounded-card" aria-labelledby="published-knowledge-title">
      <div className="flex flex-col gap-base border-b border-border-subtle bg-surface-sunken/45 px-comfortable py-base lg:flex-row lg:items-end lg:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-tight">
            <BookOpen className="h-5 w-5 text-brand-primary" aria-hidden="true" />
          <h2 id="published-knowledge-title" className="text-h3 text-text-primary">已发布/有效知识</h2>
          </div>
          <p className="mt-micro text-caption text-text-tertiary">
            {scopeFilter ? '当前适用范围：' + scopeFilter : '默认显示当前工作区的已发布/有效知识'}
          </p>
        </div>
        <div className="flex flex-wrap items-end gap-tight">
          <Field
            label="适用范围（高级筛选，可选）"
            hint='支持 key=value 或 JSON 对象，例如 project=atlas；留空查看全部已发布/有效知识'
            error={scopeError}
            className="min-w-52"
          >
            <Input
              value={scopeDraft}
              onChange={(event) => onScopeDraftChange(event.target.value)}
              placeholder='例如 project=atlas 或 {"project":"atlas"}'
              invalid={Boolean(scopeError)}
              aria-label="按适用范围筛选知识"
            />
          </Field>
          <Field label="查看范围" className="min-w-52">
            <Select value={viewAgentId} onChange={(event) => onViewAgentChange(event.target.value)} aria-label="选择知识查看范围">
              <option value="">共享知识</option>
              {agents.map((agent) => (
                <option key={agent.id} value={agent.id}>{agent.name} 的私有视图</option>
              ))}
            </Select>
          </Field>
          <Button type="button" size="sm" onClick={onApplyScope}>筛选</Button>
        </div>
      </div>

      {state.kind === 'loading' ? (
        <div className="p-comfortable"><ListRowsSkeleton /></div>
      ) : state.kind === 'error' ? (
        <InlineError message={state.message} onRetry={onRetry} />
      ) : state.value.items.length === 0 ? (
        <EmptyState
          icon={<BookOpen className="h-5 w-5" aria-hidden="true" />}
          title="暂无已发布/有效知识"
          description={scopeFilter ? '没有匹配当前适用范围的已发布知识；可以清空筛选或发起调查。' : '候选通过整理和发布后，会出现在这里。'}
        />
      ) : (
        <div>
          <div className="overflow-x-auto" role="table" aria-label="已发布/有效知识列表">
            <div className="min-w-[760px]">
              <div role="row" className="grid grid-cols-[9rem_minmax(12rem,1.6fr)_8rem_minmax(10rem,1fr)_6rem_9rem] gap-base border-b border-border-subtle bg-surface-sunken px-comfortable py-tight text-caption font-medium text-text-tertiary">
                <span role="columnheader">ID</span>
                <span role="columnheader">标题</span>
                <span role="columnheader">类型</span>
                <span role="columnheader">适用范围</span>
                <span role="columnheader">版本</span>
                <span role="columnheader">更新</span>
              </div>
              <div>
                {state.value.items.map((item) => (
                  <button
                    type="button"
                    key={item.id}
                    role="row"
                    onClick={() => onOpen(item)}
                    aria-label={'查看知识条目 ' + item.title}
                    className="grid w-full grid-cols-[9rem_minmax(12rem,1.6fr)_8rem_minmax(10rem,1fr)_6rem_9rem] gap-base border-b border-border-subtle/75 px-comfortable py-snug text-left transition-colors duration-inkFast last:border-b-0 hover:bg-surface-base focus-visible:bg-surface-base"
                  >
                    <span role="cell" className="truncate pt-0.5 font-mono text-caption text-text-secondary" title={item.id}>{item.id}</span>
                    <span role="cell" className="flex min-w-0 items-center gap-tight">
                      <span className="min-w-0">
                        <span className="block truncate text-body font-medium text-text-primary">{item.title}</span>
                        {item.summary ? <span className="mt-micro block truncate text-caption text-text-tertiary">{item.summary}</span> : null}
                      </span>
                      <ChevronRight className="h-4 w-4 shrink-0 text-text-tertiary" aria-hidden="true" />
                    </span>
                    <span role="cell" className="truncate text-caption text-text-secondary">{item.kind}</span>
                    <span role="cell" className="truncate text-caption text-text-secondary" title={formatKnowledgeScope(item.scope)}>{formatKnowledgeScope(item.scope)}</span>
                    <span role="cell" className="font-mono text-caption tabular-nums text-text-secondary">v{item.current_version}</span>
                    <time role="cell" dateTime={item.updated_at} className="truncate text-caption tabular-nums text-text-tertiary">{formatDateTime(item.updated_at)}</time>
                  </button>
                ))}
              </div>
            </div>
          </div>
          {state.value.next_cursor ? (
            <div className="flex flex-wrap items-center justify-between gap-snug border-t border-border-subtle bg-surface-sunken/35 px-comfortable py-snug">
              <div className="min-w-0 text-caption text-text-tertiary">
                <p>已加载 {state.value.items.length} 条，仍有更多知识条目。</p>
                {loadMoreError ? <p className="mt-micro text-status-error" role="alert">{loadMoreError}</p> : null}
              </div>
              <Button type="button" size="sm" onClick={onLoadMore} disabled={loadingMore}>
                <RefreshCw className={cx('h-3.5 w-3.5', loadingMore && 'animate-spin')} aria-hidden="true" />
                {loadingMore ? '加载中…' : '加载更多'}
              </Button>
            </div>
          ) : null}
        </div>
      )}
    </section>
  );
}

function SubmissionInbox({
  state,
  actionId,
  onRetry,
  onAction,
}: {
  state: KnowledgeListState<KnowledgeSubmission>;
  actionId: string | null;
  onRetry: () => void;
  onAction: (submission: KnowledgeSubmission, action: 'curate' | 'publish') => void;
}) {
  return (
    <section className="ink-paper-panel min-w-0 overflow-hidden rounded-card" aria-labelledby="knowledge-inbox-title">
      <div className="flex items-center justify-between gap-base border-b border-border-subtle bg-surface-sunken/45 px-comfortable py-base">
        <div>
          <div className="flex items-center gap-tight">
            <Inbox className="h-5 w-5 text-brand-primary" aria-hidden="true" />
            <h2 id="knowledge-inbox-title" className="text-h3 text-text-primary">候选收件箱</h2>
          </div>
          <p className="mt-micro text-caption text-text-tertiary">任务结果先进入候选；整理和发布分开显示。</p>
        </div>
        {state.kind === 'ready' ? (
          <div className="flex flex-wrap items-center justify-end gap-tight text-caption tabular-nums text-text-tertiary">
            <span>已加载 {state.value.items.length} 条</span>
            {state.value.truncated ? (
              <StatusPill className="border-status-warning/35 bg-status-warning/5 text-status-warning" title="服务端返回数量受限，列表未完整加载">
                仅显示部分
              </StatusPill>
            ) : null}
          </div>
        ) : null}
      </div>
      {state.kind === 'loading' ? (
        <div className="p-comfortable"><ListRowsSkeleton count={3} /></div>
      ) : state.kind === 'error' ? (
        <InlineError message={state.message} onRetry={onRetry} />
      ) : state.value.items.length === 0 ? (
        <EmptyState
          icon={<Inbox className="h-5 w-5" aria-hidden="true" />}
          title="收件箱为空"
          description="Agent 提交带证据的增量后，候选会出现在这里。"
        />
      ) : (
        <div className="divide-y divide-border-subtle">
          {state.value.items.map((submission) => {
            const changes = submission.request?.changes ?? [];
            const curateBusy = actionId === submission.id + ':curate';
            const publishBusy = actionId === submission.id + ':publish';
            const canCurate = submission.status === 'received' || submission.status === 'needs_review';
            const canPublish = canPublishKnowledgeSubmission(submission);
            const readOnlyPublication = submission.status === 'accepted' || submission.status === 'merged';
            const reason = submission.error_message
              ?? (submission.status === 'needs_review' ? '缺少证据或存在版本冲突，整理后再发布。' : undefined);
            const submissionHasError = submission.status === 'rejected' || (submission.error_message?.includes('冲突') ?? false);
            return (
              <div key={submission.id} className="space-y-snug px-comfortable py-base">
                <div className="flex flex-wrap items-start justify-between gap-snug">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-tight">
                      <span className="font-mono text-caption text-text-secondary">{submission.id}</span>
                      <StatusPill className={knowledgeStatusClass(submission.status)}>{statusLabel(submission.status)}</StatusPill>
                    </div>
                    <p className="mt-tight text-body text-text-primary">
                      {submission.request?.no_change ? '本次任务没有需要沉淀的变化' : changes.length + ' 个候选变化'}
                    </p>
                    <p className="mt-micro text-caption text-text-tertiary">
                      来源 Agent {submission.agent_id} · {formatDateTime(submission.created_at)}
                    </p>
                  </div>
                  <div className="flex shrink-0 flex-wrap gap-tight">
                    <Button
                      type="button"
                      size="sm"
                      onClick={() => onAction(submission, 'curate')}
                      disabled={!canCurate || Boolean(actionId)}
                    >
                      <RefreshCw className={cx('h-3.5 w-3.5', curateBusy && 'animate-spin')} aria-hidden="true" />
                      {curateBusy ? '整理中…' : '启动整理'}
                    </Button>
                    {readOnlyPublication ? (
                      <StatusPill className={knowledgeStatusClass(submission.status)}>
                        {submission.status === 'accepted' ? '已发布' : '已合并'}
                      </StatusPill>
                    ) : (
                      <Button
                        type="button"
                        size="sm"
                        variant="primary"
                        onClick={() => onAction(submission, 'publish')}
                        disabled={!canPublish || Boolean(actionId)}
                        title={!canPublish ? reason ?? '先完成整理并通过发布门' : undefined}
                      >
                        <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
                        {publishBusy ? '发布中…' : '发布'}
                      </Button>
                    )}
                  </div>
                </div>
                {reason ? (
                  <div className={cx('flex items-start gap-tight rounded-button border px-snug py-tight text-caption', submissionHasError ? 'border-status-error/30 bg-status-error/5 text-status-error' : 'border-status-warning/35 bg-status-warning/5 text-status-warning')}>
                    {submissionHasError ? <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" /> : <CircleHelp className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />}
                    <span>{reason}</span>
                  </div>
                ) : null}
                {changes.length > 0 ? (
                  <details className="group rounded-button border border-border-subtle bg-surface-base/45">
                    <summary className="cursor-pointer list-none px-snug py-tight text-caption text-text-secondary focus-visible:ring-2 focus-visible:ring-brand-primary/40">
                      <span className="mr-tight text-brand-primary">＋</span>
                      查看候选变化与证据（{changes.length}）
                    </summary>
                    <div className="space-y-snug border-t border-border-subtle px-snug py-snug">
                      {changes.map((change, index) => (
                        <div key={(change.item_id ?? 'new') + ':' + index} className="space-y-micro">
                          <div className="flex flex-wrap items-center gap-tight">
                            <span className="font-mono text-caption text-text-secondary">{change.item_id ?? '新条目'}</span>
                            <span className="text-caption text-text-tertiary">base v{change.base_version}</span>
                            <span className="text-caption text-text-tertiary">{knowledgeKindLabel(change.kind)}</span>
                          </div>
                          <p className="text-body font-medium text-text-primary">{change.title}</p>
                          <p className="line-clamp-3 whitespace-pre-wrap text-caption text-text-secondary">{change.body}</p>
                          <div className="flex flex-wrap gap-tight text-caption text-text-tertiary">
                            {change.sources?.map((source, sourceIndex) => (
                              <span key={source.ref + ':' + sourceIndex} className="inline-flex items-center gap-micro">
                                <FileText className="h-3 w-3" aria-hidden="true" />
                                {sourceKindLabel(source.kind)}：{source.ref}
                                <StatusPill className={knowledgeStatusClass('missing')}>待核验</StatusPill>
                              </span>
                            ))}
                            {change.relations?.map((relation, relationIndex) => (
                              <span key={relation.to_item_id + ':' + relationIndex} className="inline-flex items-center gap-micro">
                                <GitBranch className="h-3 w-3" aria-hidden="true" />
                                {relationLabel(relation.kind)} {relation.to_item_id}
                              </span>
                            ))}
                          </div>
                        </div>
                      ))}
                    </div>
                  </details>
                ) : null}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function KnowledgeJobsSection({
  state,
  selectedJobId,
  actionId,
  onRetry,
  onSelect,
  onCancel,
}: {
  state: KnowledgeListState<KnowledgeJob>;
  selectedJobId: string | null;
  actionId: string | null;
  onRetry: () => void;
  onSelect: (jobId: string) => void;
  onCancel: (job: KnowledgeJob) => void;
}) {
  const selected = state.kind === 'ready' ? state.value.items.find((job) => job.id === selectedJobId) ?? null : null;
  return (
    <section className="ink-paper-panel min-w-0 overflow-hidden rounded-card" aria-labelledby="knowledge-jobs-title">
      <div className="flex items-center justify-between gap-base border-b border-border-subtle bg-surface-sunken/45 px-comfortable py-base">
        <div>
          <div className="flex items-center gap-tight">
            <Network className="h-5 w-5 text-brand-primary" aria-hidden="true" />
            <h2 id="knowledge-jobs-title" className="text-h3 text-text-primary">调查作业</h2>
          </div>
          <p className="mt-micro text-caption text-text-tertiary">查看调查进度、覆盖表、缺口和逐条引用。</p>
        </div>
        {state.kind === 'ready' ? (
          <div className="flex flex-wrap items-center justify-end gap-tight text-caption tabular-nums text-text-tertiary">
            <span>已加载 {state.value.items.length} 条</span>
            {state.value.truncated ? (
              <StatusPill className="border-status-warning/35 bg-status-warning/5 text-status-warning" title="服务端返回数量受限，列表未完整加载">
                仅显示部分
              </StatusPill>
            ) : null}
          </div>
        ) : null}
      </div>
      {state.kind === 'loading' ? (
        <div className="p-comfortable"><ListRowsSkeleton count={3} /></div>
      ) : state.kind === 'error' ? (
        <InlineError message={state.message} onRetry={onRetry} />
      ) : state.value.items.length === 0 ? (
        <EmptyState
          icon={<Network className="h-5 w-5" aria-hidden="true" />}
          title="还没有调查作业"
          description="从上方发起一个自然语言调查，作业结果会保留在这里。"
        />
      ) : (
        <div className="grid min-w-0 lg:grid-cols-[minmax(10rem,0.72fr)_minmax(0,1.28fr)]">
          <div className="divide-y divide-border-subtle border-b border-border-subtle lg:border-b-0 lg:border-r">
            {state.value.items.map((job) => (
              <button
                type="button"
                key={job.id}
                onClick={() => onSelect(job.id)}
                aria-pressed={job.id === selectedJobId}
                className={cx('w-full px-base py-snug text-left transition-colors duration-inkFast hover:bg-surface-base focus-visible:bg-surface-base', job.id === selectedJobId && 'bg-brand-muted')}
              >
                <div className="flex items-center justify-between gap-tight">
                  <span className="truncate font-mono text-caption text-text-secondary">{job.id}</span>
                  <StatusPill className={knowledgeStatusClass(job.status)}>{statusLabel(job.status)}</StatusPill>
                </div>
                <p className="mt-tight line-clamp-2 text-body text-text-primary">{job.question}</p>
                <p className="mt-micro text-caption text-text-tertiary">{formatDateTime(job.updated_at)}</p>
              </button>
            ))}
          </div>
          <div className="min-w-0 p-comfortable">
            {selected ? (
              <KnowledgeJobDetail job={selected} actionId={actionId} onCancel={() => onCancel(selected)} />
            ) : (
              <EmptyState icon={<CircleHelp className="h-5 w-5" aria-hidden="true" />} title="选择一个调查作业" description="这里会显示结构化知识包和覆盖结果。" />
            )}
          </div>
        </div>
      )}
    </section>
  );
}

function KnowledgeJobDetail({
  job,
  actionId,
  onCancel,
}: {
  job: KnowledgeJob;
  actionId: string | null;
  onCancel: () => void;
}) {
  const coverage = jobCoverage(job);
  const result = job.result;
  const answer = typeof result?.answer === 'string' ? result.answer : typeof result?.summary === 'string' ? result.summary : '';
  const packageValue = result?.knowledge_package ?? result?.package;
  const citations = Array.isArray(result?.citations) ? result.citations : [];
  const gaps = Array.isArray(result?.gaps) ? result.gaps : [];
  const running = !isKnowledgeJobTerminal(job.status);
  return (
    <div className="space-y-comfortable">
      <div className="flex flex-wrap items-start justify-between gap-base">
        <div className="min-w-0">
          <p className="font-mono text-caption text-text-tertiary">{job.id}</p>
          <h3 className="mt-micro text-body-lg font-medium text-text-primary">{job.question}</h3>
          <p className="mt-micro text-caption text-text-tertiary">
            第 {job.used.turns} / {job.budget.max_turns} 轮 · 搜索 {job.used.searches} 次 · 阅读 {job.used.reads} 条 · 关系 {job.used.relations} 条
          </p>
        </div>
        {running ? (
          <Button type="button" size="sm" variant="danger-outline" onClick={onCancel} disabled={actionId === job.id}>
            <XCircle className="h-3.5 w-3.5" aria-hidden="true" />
            {actionId === job.id ? '取消中…' : '取消调查'}
          </Button>
        ) : null}
      </div>

      {job.last_error ? (
        <div className="flex items-start gap-tight rounded-button border border-status-error/30 bg-status-error/5 px-snug py-tight text-caption text-status-error" role="alert">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <span>{job.last_error}</span>
        </div>
      ) : null}

      <CoveragePanel coverage={coverage} />

      {answer ? (
        <section aria-labelledby="knowledge-answer-title">
          <SectionHeading id="knowledge-answer-title" icon={<ShieldCheck className="h-4 w-4" aria-hidden="true" />} title="调查结论" />
          <div className="mt-snug rounded-card border border-border-subtle bg-surface-base/55 px-base py-snug">
            <AgentOutput text={answer} showCaret={false} />
          </div>
        </section>
      ) : null}

      {packageValue !== undefined ? (
        <section aria-labelledby="knowledge-package-title">
          <SectionHeading id="knowledge-package-title" icon={<BookOpen className="h-4 w-4" aria-hidden="true" />} title="结构化知识包" />
          <KnowledgePackage value={packageValue} />
        </section>
      ) : null}

      <section aria-labelledby="knowledge-citations-title">
        <SectionHeading id="knowledge-citations-title" icon={<FileText className="h-4 w-4" aria-hidden="true" />} title="引用与缺口" />
        <div className="mt-snug space-y-snug">
          {citations.length > 0 ? (
            <div className="space-y-tight">
              {citations.map((citation, index) => (
                <div key={(citation.version_id ?? citation.knowledge_id ?? 'citation') + ':' + index} className="rounded-button border border-border-subtle bg-surface-base/55 px-snug py-tight">
                  <div className="flex flex-wrap items-center gap-tight">
                    <span className="font-mono text-caption text-text-secondary">{citation.knowledge_id ?? citation.item_id ?? '来源'}</span>
                    {citation.version_id ? <span className="font-mono text-caption text-text-tertiary">{citation.version_id}</span> : null}
                    {citation.status ? <StatusPill className={knowledgeStatusClass(citation.status)}>{statusLabel(citation.status)}</StatusPill> : null}
                  </div>
                  {citation.excerpt ? <p className="mt-micro whitespace-pre-wrap text-caption text-text-secondary">{citation.excerpt}</p> : null}
                  {citation.locator ? <p className="mt-micro font-mono text-caption text-text-tertiary">{citation.locator}</p> : null}
                </div>
              ))}
            </div>
          ) : (
            <p className="text-caption text-text-tertiary">服务端未返回逐条引用。</p>
          )}
          {gaps.length > 0 ? (
            <div className="rounded-button border border-status-warning/35 bg-status-warning/5 px-snug py-tight">
              <p className="text-caption font-medium text-status-warning">待补查</p>
              <ul className="mt-micro list-disc space-y-micro pl-5 text-caption text-text-secondary">
                {gaps.map((gap, index) => <li key={gap + ':' + index}>{gap}</li>)}
              </ul>
            </div>
          ) : null}
          {!answer && packageValue === undefined && citations.length === 0 && gaps.length === 0 && job.observations?.length === 0 ? (
            <p className="text-caption text-text-tertiary">作业尚未返回知识包；请查看当前状态和覆盖表。</p>
          ) : null}
        </div>
      </section>
    </div>
  );
}

function CoveragePanel({ coverage }: { coverage: KnowledgeCoverage }) {
  const entries = coverage.entries ?? [];
  return (
    <section aria-labelledby="knowledge-coverage-title">
      <div className="flex flex-wrap items-center justify-between gap-tight">
        <SectionHeading id="knowledge-coverage-title" icon={<CheckCircle2 className="h-4 w-4" aria-hidden="true" />} title="覆盖检查" />
        <StatusPill className={knowledgeStatusClass(coverage.status)}>
          {statusLabel(coverage.status)}{coverage.truncated ? ' · 已截断' : ''}
        </StatusPill>
      </div>
      <div className="mt-snug rounded-card border border-border-subtle bg-surface-sunken/45 px-snug py-snug">
        <div className="flex flex-wrap gap-base text-caption text-text-secondary">
          <span>访问 {coverage.visited_nodes} 个条目</span>
          <span>展开 {coverage.visited_relations} 条关系</span>
          <span>上限 {coverage.budget.max_nodes} 节点 / {coverage.budget.max_relations} 关系</span>
        </div>
        {entries.length > 0 ? (
          <div className="mt-snug divide-y divide-border-subtle">
            {entries.map((entry) => <CoverageRow key={entry.subject} entry={entry} />)}
          </div>
        ) : (
          <p className="mt-snug text-caption text-text-tertiary">服务端尚未返回覆盖明细。</p>
        )}
      </div>
    </section>
  );
}

function CoverageRow({ entry }: { entry: KnowledgeCoverageEntry }) {
  const icon = entry.status === 'complete'
    ? <CheckCircle2 className="h-4 w-4 text-status-success" aria-hidden="true" />
    : entry.status === 'conflict'
      ? <AlertCircle className="h-4 w-4 text-status-error" aria-hidden="true" />
      : <CircleHelp className="h-4 w-4 text-status-warning" aria-hidden="true" />;
  return (
    <div className="flex items-start gap-tight py-tight first:pt-0 last:pb-0">
      <span className="mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-tight">
          <span className="text-caption font-medium text-text-primary">{entry.subject}</span>
          <StatusPill className={knowledgeStatusClass(entry.status)}>{statusLabel(entry.status)}</StatusPill>
        </div>
        {entry.note ? <p className="mt-micro text-caption text-text-secondary">{entry.note}</p> : null}
        {entry.missing?.length ? <p className="mt-micro text-caption text-status-warning">缺口：{entry.missing.join('、')}</p> : null}
        {entry.related_item_ids?.length ? <p className="mt-micro font-mono text-caption text-text-tertiary">关联：{entry.related_item_ids.join('、')}</p> : null}
      </div>
    </div>
  );
}

function KnowledgePackage({ value }: { value: Record<string, unknown> | string }) {
  if (typeof value === 'string') {
    return (
      <div className="mt-snug rounded-card border border-border-subtle bg-surface-base/55 px-base py-snug">
        <AgentOutput text={value} showCaret={false} />
      </div>
    );
  }
  const entries = Object.entries(value);
  return (
    <div className="mt-snug overflow-hidden rounded-card border border-border-subtle bg-surface-base/55">
      {entries.length === 0 ? (
        <p className="px-base py-snug text-caption text-text-tertiary">知识包为空。</p>
      ) : (
        <dl className="divide-y divide-border-subtle">
          {entries.map(([key, item]) => (
            <div key={key} className="grid gap-tight px-base py-snug md:grid-cols-[8rem_minmax(0,1fr)]">
              <dt className="font-mono text-caption text-text-tertiary">{key === 'no_change' ? '无变更' : key}</dt>
              <dd className="min-w-0 whitespace-pre-wrap text-caption text-text-secondary">
                {key === 'no_change' && typeof item === 'boolean'
                  ? item ? '是' : '否'
                  : typeof item === 'string' || typeof item === 'number' || typeof item === 'boolean'
                    ? String(item)
                  : JSON.stringify(item, null, 2)}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}

export type KnowledgeSourceVerificationLabel =
  | '已核验运行记录'
  | '已核验元信息'
  | '已核验任务引用'
  | '已登记摘录'
  | '未核验代码/测试'
  | '仅核验身份'
  | '待核验';

export function sourceVerificationLabel(source: KnowledgeSource): KnowledgeSourceVerificationLabel {
  const metadata = source.metadata;
  if (!source.id) return '待核验';
  switch (metadata?.verification) {
    case 'run_output_verified':
      return metadata.digest_verified === true ? '已核验运行记录' : '待核验';
    case 'artifact_manifest_verified':
      return metadata.content_read === false ? '已核验元信息' : '待核验';
    case 'work_item_reference_verified':
      return '已核验任务引用';
    case 'submitted_excerpt':
      return '已登记摘录';
    case 'submitted_excerpt_not_repository_verified':
      return '未核验代码/测试';
    case 'agent_identity_only':
      return '仅核验身份';
    default:
      return '待核验';
  }
}

function KnowledgeDetailDrawer({
  workspaceId,
  item,
  agentId,
  onClose,
  onRepealed,
}: {
  workspaceId: string;
  item: KnowledgeItem;
  agentId?: string;
  onClose: () => void;
  onRepealed: (item: KnowledgeItem) => void;
}) {
  const [bundleState, setBundleState] = useState<AsyncState<KnowledgeItemDetails>>({ kind: 'loading' });
  const [versionsState, setVersionsState] = useState<AsyncState<{ items: KnowledgeVersion[] }>>({ kind: 'loading' });
  const [relationsState, setRelationsState] = useState<AsyncState<{ items: KnowledgeRelation[] }>>({ kind: 'loading' });
  const [selectedVersion, setSelectedVersion] = useState(String(item.current_version));
  const [versionLoading, setVersionLoading] = useState(false);
  const [reloadNonce, setReloadNonce] = useState(0);
  const [repealReason, setRepealReason] = useState('');
  const [repealError, setRepealError] = useState<string | undefined>();
  const [repealSubmitting, setRepealSubmitting] = useState(false);
  const request = useRef(0);

  useEffect(() => {
    const scope = captureScope();
    const current = ++request.current;
    setBundleState({ kind: 'loading' });
    setVersionsState({ kind: 'loading' });
    setRelationsState({ kind: 'loading' });
    setSelectedVersion(String(item.current_version));
    setRepealReason('');
    setRepealError(undefined);
    setRepealSubmitting(false);
    void Promise.allSettled([
      getKnowledgeItem(workspaceId, item.id, agentId),
      listKnowledgeVersions(workspaceId, item.id, agentId),
      listKnowledgeRelations(workspaceId, item.id, 'both', agentId),
    ]).then(([bundleResult, versionsResult, relationsResult]) => {
      if (current !== request.current || !isCurrent(scope)) return;
      if (bundleResult.status === 'fulfilled') setBundleState({ kind: 'ready', value: bundleResult.value });
      else setBundleState({ kind: 'error', message: errorMessage(bundleResult.reason, '知识原文加载失败') });
      if (versionsResult.status === 'fulfilled') setVersionsState({ kind: 'ready', value: versionsResult.value });
      else setVersionsState({ kind: 'error', message: errorMessage(versionsResult.reason, '知识版本加载失败') });
      if (relationsResult.status === 'fulfilled') setRelationsState({ kind: 'ready', value: relationsResult.value });
      else setRelationsState({ kind: 'error', message: errorMessage(relationsResult.reason, '知识关系加载失败') });
    });
  }, [agentId, item.current_version, item.id, reloadNonce, workspaceId]);

  const bundle = bundleState.kind === 'ready' ? bundleState.value : null;
  const versions = versionsState.kind === 'ready' ? versionsState.value.items : [];
  const relations = relationsState.kind === 'ready' ? relationsState.value.items : [];

  const readVersion = async (version: string) => {
    if (version === String(bundle?.version.version ?? item.current_version) || versionLoading) return;
    const scope = captureScope();
    setVersionLoading(true);
    try {
      const next = await getKnowledgeVersion(workspaceId, item.id, version, agentId);
      if (!isCurrent(scope)) return;
      setBundleState({ kind: 'ready', value: next });
      setSelectedVersion(version);
    } catch (error) {
      if (isCurrent(scope)) toast.error(errorMessage(error, '知识版本读取失败'));
    } finally {
      if (isCurrent(scope)) setVersionLoading(false);
    }
  };

  const handleRepeal = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const reason = repealReason.trim();
    if (!reason || repealSubmitting || !bundle) return;
    if (!window.confirm('废止后该条目将从默认知识检索中移除，历史版本仍可查看。确认废止？')) return;
    const scope = captureScope();
    setRepealSubmitting(true);
    setRepealError(undefined);
    try {
      const next = await repealKnowledgeItem(workspaceId, item.id, {
        expected_version: bundle.item.current_version,
        reason,
      }, agentId);
      if (!isCurrent(scope)) return;
      setBundleState((state) => state.kind === 'ready'
        ? { kind: 'ready', value: { ...state.value, item: next, version: { ...state.value.version, status: next.status } } }
        : state);
      setRepealReason('');
      onRepealed(next);
      toast.info('知识条目已废止，历史版本仍可查看');
    } catch (error) {
      if (isCurrent(scope)) setRepealError(errorMessage(error, '知识条目废止失败'));
    } finally {
      if (isCurrent(scope)) setRepealSubmitting(false);
    }
  };

  return (
    <Drawer open title="知识条目详情" onClose={onClose} width={560} ariaLabel={'知识条目 ' + item.title}>
      <div className="space-y-comfortable p-comfortable">
        {bundleState.kind === 'loading' ? (
          <div className="space-y-snug">
            <Skeleton className="h-6 w-2/3" />
            <Skeleton className="h-24 w-full" />
            <Skeleton className="h-16 w-full" />
          </div>
        ) : bundleState.kind === 'error' ? (
          <InlineError message={bundleState.message} onRetry={() => setReloadNonce((value) => value + 1)} />
        ) : bundle ? (
          <>
            <div>
              <div className="flex flex-wrap items-center gap-tight">
                <span className="font-mono text-caption text-text-secondary">{bundle.item.id}</span>
                <StatusPill className={knowledgeStatusClass(bundle.version.status)}>{statusLabel(bundle.version.status)}</StatusPill>
              </div>
              <h2 className="mt-tight text-h2 text-text-primary">{bundle.version.title}</h2>
              {bundle.version.summary ? <p className="mt-tight text-body text-text-secondary">{bundle.version.summary}</p> : null}
              <div className="mt-snug flex flex-wrap items-center gap-tight text-caption text-text-tertiary">
                <span>{bundle.version.kind}</span>
                <span aria-hidden="true">·</span>
                <span className="font-mono">v{bundle.version.version}</span>
                <span aria-hidden="true">·</span>
                <span>{formatKnowledgeScope(bundle.version.scope)}</span>
              </div>
            </div>

            <Field label="查看版本">
              <Select
                value={selectedVersion}
                onChange={(event) => void readVersion(event.target.value)}
                disabled={versionLoading || versionsState.kind === 'loading'}
                aria-label="选择知识版本"
              >
                {[...versions].sort((a, b) => b.version - a.version).map((version) => (
                  <option key={version.id} value={String(version.version)}>
                    v{version.version} · {statusLabel(version.status)}
                  </option>
                ))}
                {versions.length === 0 ? <option value={String(bundle.version.version)}>v{bundle.version.version}</option> : null}
              </Select>
            </Field>

            <section aria-labelledby="knowledge-body-title">
              <SectionHeading id="knowledge-body-title" icon={<FileText className="h-4 w-4" aria-hidden="true" />} title="原文" />
              <div className="mt-snug rounded-card border border-border-subtle bg-surface-base/55 px-base py-snug">
                {bundle.version.body_markdown ? <AgentOutput text={bundle.version.body_markdown} showCaret={false} /> : <p className="text-caption text-text-tertiary">该版本没有正文。</p>}
              </div>
            </section>

            <SourceList sources={bundle.sources} />
            <RelationList relations={relations} loading={relationsState.kind === 'loading'} />

            <section aria-labelledby="knowledge-lineage-title">
              <SectionHeading id="knowledge-lineage-title" icon={<ShieldCheck className="h-4 w-4" aria-hidden="true" />} title="谱系" />
              <dl className="mt-snug grid gap-snug rounded-card border border-border-subtle bg-surface-sunken/45 px-base py-snug text-caption md:grid-cols-2">
                <LineageField label="创建 Agent" value={bundle.version.created_by_agent_id} />
                <LineageField label="来源 Run" value={bundle.version.created_by_run_id} />
                <LineageField label="来源任务" value={bundle.version.created_by_work_item_id} />
                <LineageField label="内容摘要" value={bundle.version.content_digest} mono />
              </dl>
            </section>

            {bundle.item.status === 'effective' ? (
              <section aria-labelledby="knowledge-repeal-title">
                <SectionHeading id="knowledge-repeal-title" icon={<XCircle className="h-4 w-4" aria-hidden="true" />} title="废止条目" />
                <form className="mt-snug space-y-snug rounded-card border border-status-error/25 bg-status-error/5 px-base py-snug" onSubmit={handleRepeal}>
                  <Field
                    label="废止原因"
                    hint="废止会从默认检索中移除当前条目，历史版本和来源仍保留。"
                    error={repealError}
                  >
                    <Textarea
                      value={repealReason}
                      onChange={(event) => {
                        setRepealReason(event.target.value);
                        setRepealError(undefined);
                      }}
                      placeholder="例如：该规则已被新的产品决策取代"
                      rows={3}
                      required
                      invalid={Boolean(repealError)}
                      aria-label="填写知识条目废止原因"
                    />
                  </Field>
                  <div className="flex flex-wrap items-center justify-between gap-snug">
                    <p className="max-w-sm text-caption text-status-error">这是不可逆的状态变更，请确认原因准确且可追溯。</p>
                    <Button type="submit" variant="danger-outline" disabled={repealSubmitting || !repealReason.trim()}>
                      <XCircle className="h-3.5 w-3.5" aria-hidden="true" />
                      {repealSubmitting ? '废止中…' : '确认废止'}
                    </Button>
                  </div>
                </form>
              </section>
            ) : (
              <div className="flex items-start gap-tight rounded-button border border-status-warning/30 bg-status-warning/5 px-snug py-tight text-caption text-status-warning">
                <CircleHelp className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
                <span>该条目已废止；历史版本仍可从上方版本选择器中查看。</span>
              </div>
            )}
          </>
        ) : null}
      </div>
    </Drawer>
  );
}

function SourceList({ sources }: { sources: KnowledgeItemDetails['sources'] }) {
  return (
    <section aria-labelledby="knowledge-sources-title">
      <SectionHeading id="knowledge-sources-title" icon={<FileText className="h-4 w-4" aria-hidden="true" />} title="来源证据" />
      <div className="mt-snug space-y-tight">
        {sources.length === 0 ? <p className="text-caption text-text-tertiary">该版本没有登记来源证据。</p> : sources.map((source) => {
          const verification = sourceVerificationLabel(source);
          return (
            <div key={source.id} className="rounded-button border border-border-subtle bg-surface-sunken/45 px-snug py-tight">
              <div className="flex flex-wrap items-center gap-tight">
                <span className="font-mono text-caption text-text-secondary">{sourceKindLabel(source.kind)}</span>
                {verification ? (
                <StatusPill className={knowledgeStatusClass(verification.startsWith('已核验') ? 'complete' : 'missing')}>
                    {verification}
                  </StatusPill>
                ) : null}
                <span className="min-w-0 truncate text-caption text-text-primary" title={source.ref}>{source.ref}</span>
              </div>
              {source.locator ? <p className="mt-micro font-mono text-caption text-text-tertiary">{source.locator}</p> : null}
              {source.excerpt ? <p className="mt-micro whitespace-pre-wrap text-caption text-text-secondary">{source.excerpt}</p> : null}
            </div>
          );
        })}
      </div>
    </section>
  );
}

function RelationList({ relations, loading }: { relations: KnowledgeRelation[]; loading: boolean }) {
  return (
    <section aria-labelledby="knowledge-relations-title">
      <div className="flex items-center justify-between gap-tight">
        <SectionHeading id="knowledge-relations-title" icon={<GitBranch className="h-4 w-4" aria-hidden="true" />} title="正反向关系" />
        <span className="text-caption tabular-nums text-text-tertiary">{loading ? '读取中…' : relations.length + ' 条'}</span>
      </div>
      <div className="mt-snug space-y-tight">
        {!loading && relations.length === 0 ? <p className="text-caption text-text-tertiary">没有可见的正反向关系。</p> : null}
        {relations.map((relation) => (
          <div key={relation.id} className="rounded-button border border-border-subtle bg-surface-sunken/45 px-snug py-tight">
            <div className="flex flex-wrap items-center gap-tight text-caption">
              <span className="font-mono text-text-secondary">{relation.from_item_id}</span>
              <span className="text-brand-primary">{relationLabel(relation.kind)}</span>
              <span className="font-mono text-text-secondary">{relation.to_item_id}</span>
            </div>
            {relation.condition ? <p className="mt-micro text-caption text-text-secondary">条件：{relation.condition}</p> : null}
            {relation.rationale ? <p className="mt-micro text-caption text-text-tertiary">依据：{relation.rationale}</p> : null}
          </div>
        ))}
      </div>
    </section>
  );
}

function LineageField({ label, value, mono = false }: { label: string; value?: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-text-tertiary">{label}</dt>
      <dd className={cx('mt-micro truncate text-text-secondary', mono && 'font-mono')} title={value ?? '未记录'}>{value ?? '未记录'}</dd>
    </div>
  );
}

function SectionHeading({ id, icon, title }: { id: string; icon: ReactNode; title: string }) {
  return (
    <h3 id={id} className="flex items-center gap-tight text-body font-medium text-text-primary">
      <span className="text-brand-primary" aria-hidden="true">{icon}</span>
      {title}
    </h3>
  );
}

function InlineError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-base px-comfortable py-base" role="alert">
      <div className="flex min-w-0 items-start gap-tight">
        <AlertCircle className="mt-0.5 h-5 w-5 shrink-0 text-status-error" aria-hidden="true" />
        <p className="text-body text-text-secondary">{message}</p>
      </div>
      <Button type="button" size="sm" onClick={onRetry}>重试</Button>
    </div>
  );
}

function ListRowsSkeleton({ count = 5 }: { count?: number }) {
  return (
    <div className="space-y-tight" role="status" aria-label="列表加载中">
      {Array.from({ length: count }, (_, index) => <Skeleton key={index} className="h-10 w-full rounded-button" />)}
    </div>
  );
}
