import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, beforeEach } from 'vitest';
import type { GovernanceView } from '../../stores/governance.store';
import { useGovernanceStore } from '../../stores/governance.store';
import { GovernanceTimeline } from './governance-panel';

const view: GovernanceView = {
  goal: {
    id: 'goal_01ARZ3NDEKTSV4RRFFQ69G5FAV', workspace_id: 'ws_1', root_work_item_id: 'wi_root',
    objective: '完成治理控制面', acceptance_contract: ['证据可回放'], status: 'active', phase: 'execution',
    current_todo_id: 'todo_01ARZ3NDEKTSV4RRFFQ69G5FAV', quota_policies: [], completion_evidence_summary: [],
    version: 2, created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:01:00Z',
  },
  todos: [{
    id: 'todo_01ARZ3NDEKTSV4RRFFQ69G5FAV', goal_id: 'goal_01ARZ3NDEKTSV4RRFFQ69G5FAV', class: 'advancement', status: 'waiting',
    instruction: '等待当前 Turn 证据', acceptance: ['证据可回放'], resume_condition: null, priority: 'medium',
    predecessors: [], successors: [], decision_scope: { work_item_ids: ['wi_root'], agent_ids: ['agent_01ARZ3NDEKTSV4RRFFQ69G5FAV'], runtime_capabilities: [], write_scopes: [], max_dispatch: 1 },
    claim: null, claim_version: 1, last_turn_seq: 3,
    completion_turn_key: null, completion_evidence_id: null,
    version: 4, created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:01:00Z',
  }],
  quota: { goal_id: 'goal_01ARZ3NDEKTSV4RRFFQ69G5FAV', policies: [], kinds: [] },
  evidence: [{ source_kind: 'run', source_id: 'run_01ARZ3NDEKTSV4RRFFQ69G5FAV', verification: 'passed', summary: 'Run passed', recorded_at: '2026-09-01T00:01:00Z' }],
  handoffs: [{
    id: 'handoff_01ARZ3NDEKTSV4RRFFQ69G5FAV', goal_id: 'goal_01ARZ3NDEKTSV4RRFFQ69G5FAV', todo_id: 'todo_01ARZ3NDEKTSV4RRFFQ69G5FAV',
    source: { kind: 'agent', id: 'agent_01ARZ3NDEKTSV4RRFFQ69G5FAV' }, target: { kind: 'agent', id: 'agent_01ARZ3NDEKTSV4RRFFQ69G5FAV' },
    reason: 'handoff', context_summary: 'summary', evidence: [], open_risks: [], status: 'transferred', claim_transfer_state: 'transferred',
    source_claim_version: 1, target_claim_version: 2, actor: { kind: 'agent', id: 'agent_01ARZ3NDEKTSV4RRFFQ69G5FAV' }, version: 2,
    created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:01:00Z',
  }],
  latest_receipt: {
    header: { turn_key: { goal_id: 'goal_01ARZ3NDEKTSV4RRFFQ69G5FAV', todo_id: 'todo_01ARZ3NDEKTSV4RRFFQ69G5FAV', turn_seq: 3 }, attempt: 1, schema_version: 'plan-decision/v2', input_snapshot_digest: 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', admission_client_key: 'test', canonical_digest: 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb', created_at: '2026-09-01T00:01:00Z' },
    phases: [{ turn_key: { goal_id: 'goal_01ARZ3NDEKTSV4RRFFQ69G5FAV', todo_id: 'todo_01ARZ3NDEKTSV4RRFFQ69G5FAV', turn_seq: 3 }, phase_seq: 5, phase: 'dispatch', payload: {}, canonical_digest: 'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc', plan_id: 'plan_01ARZ3NDEKTSV4RRFFQ69G5FAV', run_ids: ['run_01ARZ3NDEKTSV4RRFFQ69G5FAV'], created_at: '2026-09-01T00:01:00Z' }],
  },
};

describe('GovernancePanel', () => {
  beforeEach(() => {
    useGovernanceStore.setState({ byWorkItem: { wi_root: view }, statusByWorkItem: { wi_root: 'ready' }, errorByWorkItem: {} });
  });

  it('renders the server read model as one Goal→Todo→Plan→Run→Evidence→Handoff chain', () => {
    expect(useGovernanceStore.getState().byWorkItem.wi_root).toBe(view);
    const html = renderToStaticMarkup(
      <GovernanceTimeline
        goal={view.goal}
        todo={view.todos[0]}
        receipt={view.latest_receipt}
        evidence={view.evidence}
        handoffs={view.handoffs}
        evidenceHref="#delivery-brief-wi_root"
      />,
    );
    expect(html).toContain('Goal');
    expect(html).toContain('Todo');
    expect(html).toContain('Plan');
    expect(html).toContain('Run');
    expect(html).toContain('Evidence');
    expect(html).toContain('Handoff');
    expect(html).toContain('plan_01ARZ3NDEKTSV4RRFFQ69G5FAV');
    expect(html).toContain('run_01ARZ3NDEKTSV4RRFFQ69G5FAV');
  });
});
