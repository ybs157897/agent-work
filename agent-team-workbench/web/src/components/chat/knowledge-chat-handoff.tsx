import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { getKnowledgeItem, getKnowledgeVersion } from '../../api/knowledge';
import { useAgentsStore } from '../../stores/agents.store';
import { useWorkspaceStore } from '../../stores/workspace.store';
import { isKnowledgeLibrarianAgent } from '../../utils/agent-scope';
import { buildKnowledgeQuestionDraft, knowledgeReturnPath, readKnowledgeChatRequest } from '../../utils/knowledge-chat-context';

export interface KnowledgeChatSeed {
  agentId: string;
  text: string;
  title: string;
  version: number;
  returnTo: string;
}

export function KnowledgeChatHandoff({ query, onReady }: { query: string; onReady: (seed: KnowledgeChatSeed) => Promise<void> }) {
  const workspaceId = useWorkspaceStore((state) => state.workspace?.id);
  const generation = useWorkspaceStore((state) => state.generation);
  const agents = useAgentsStore((state) => state.agents);
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<{ key: string; error?: string; seed?: KnowledgeChatSeed } | null>(null);
  const delivered = useRef('');
  const params = new URLSearchParams(query);
  const hasKnowledge = params.has('knowledge');
  const itemId = params.get('knowledge') ?? '';
  const requestedAgent = params.get('agent') ?? '';
  const version = params.get('version') ?? '';
  const conversationId = params.get('c') ?? '';
  const returnTo = params.get('return_to');
  const librarian = agents.find((agent) => agent.id === requestedAgent && isKnowledgeLibrarianAgent(agent));
  const key = `${workspaceId ?? ''}:${generation}:${requestedAgent}:${itemId}:${version}:${conversationId}:${returnTo ?? ''}`;
  const current = state?.key === key ? state : null;
  const keyRef = useRef(key);
  useLayoutEffect(() => { keyRef.current = key; }, [key]);

  useEffect(() => {
    if (!workspaceId || !hasKnowledge || agents.length === 0) return;
    let active = true;
    const load = async () => {
      try {
        const request = readKnowledgeChatRequest(new URLSearchParams({ knowledge: itemId, version, ...(returnTo ? { return_to: returnTo } : {}) }));
        if (!request || !librarian) throw new Error('知识管理员入口不可用，请返回知识库重新选择。');
        const details = request.version
          ? await getKnowledgeVersion(workspaceId, request.itemId, request.version)
          : await getKnowledgeItem(workspaceId, request.itemId);
        if (!active || keyRef.current !== key) return;
        const seed: KnowledgeChatSeed = {
          agentId: librarian.id,
          text: buildKnowledgeQuestionDraft(details),
          title: details.version.title,
          version: details.version.version,
          returnTo: knowledgeReturnPath(request.returnTo, details.item.id, String(details.version.version), details.item.workspace_id),
        };
        if (delivered.current !== key) {
          await onReady(seed);
          if (!active || keyRef.current !== key) return;
          delivered.current = key;
        }
        setState({ key, seed });
      } catch (error) {
        if (active && keyRef.current === key) setState({ key, error: error instanceof Error ? error.message : '知识读取失败，请重试。' });
      }
    };
    void load();
    return () => { active = false; };
  }, [workspaceId, generation, hasKnowledge, itemId, requestedAgent, version, conversationId, returnTo, librarian, agents.length, key, attempt, onReady]);

  if (!hasKnowledge) return null;
  if (current?.error) return (
    <div className="flex flex-wrap items-center gap-snug border-b border-border-subtle bg-surface-raised px-base py-snug" role="alert">
      <span className="text-body text-status-error">{current.error}</span>
      <button type="button" className="text-body text-brand-primary underline" onClick={() => { setState(null); setAttempt((value) => value + 1); }}>重试读取知识</button>
      <Link className="text-body text-text-secondary underline" to={knowledgeReturnPath(returnTo, itemId, version, workspaceId)}>返回知识库</Link>
    </div>
  );
  return (
    <div className="flex flex-wrap items-center justify-between gap-tight border-b border-border-subtle bg-surface-raised px-base py-snug" role="status" aria-live="polite">
      <span className="min-w-0 text-body text-text-secondary">{current?.seed ? `正在讨论：${current.seed.title} · 第 ${current.seed.version} 版` : '正在读取知识引用…'}</span>
      {current?.seed && <Link to={current.seed.returnTo} className="shrink-0 text-body text-brand-primary underline">返回这条知识</Link>}
    </div>
  );
}
