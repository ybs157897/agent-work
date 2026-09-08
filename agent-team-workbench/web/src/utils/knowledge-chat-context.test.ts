import { describe, expect, it } from 'vitest';
import type { KnowledgeItemDetails } from '../api/knowledge';
import { buildKnowledgeQuestionDraft, knowledgeReturnPath, readKnowledgeChatRequest } from './knowledge-chat-context';

describe('knowledge chat handoff', () => {
  it('only accepts a knowledge item and a fixed valid version', () => {
    expect(readKnowledgeChatRequest(new URLSearchParams('agent=agent_1'))).toBeNull();
    expect(readKnowledgeChatRequest(new URLSearchParams('knowledge=kb_1&version=2'))).toMatchObject({ itemId: 'kb_1', version: '2' });
    expect(() => readKnowledgeChatRequest(new URLSearchParams('knowledge=../../runs&version=2'))).toThrow('格式无效');
    expect(() => readKnowledgeChatRequest(new URLSearchParams('knowledge=kb_1&version=-1'))).toThrow('格式无效');
    expect(() => readKnowledgeChatRequest(new URLSearchParams('knowledge=kb_1&version=NaN'))).toThrow('格式无效');
  });

  it('rejects external and non-knowledge return paths while preserving search context', () => {
    for (const path of ['https://example.com', '//example.com', '/\\example.com', '/settings', '/knowledge-elsewhere']) {
      expect(knowledgeReturnPath(path, 'kb_1', '2')).toBe('/knowledge?item=kb_1&version=2');
    }
    expect(knowledgeReturnPath('/knowledge?q=退款&kind=rule&item=kb_old&version=1&write=true', 'kb_1', '2'))
      .toBe('/knowledge?q=%E9%80%80%E6%AC%BE&kind=rule&item=kb_1&version=2');
  });

  it('uses the authorized version title and stable reference without copying the body into a new message', () => {
    const details = {
      item: { id: 'kb_1', title: '当前标题', current_version: 3 },
      version: { title: '旧版退款约定', version: 2, body_markdown: '不应自动复制的长正文' },
      sources: [],
    } as unknown as KnowledgeItemDetails;
    const draft = buildKnowledgeQuestionDraft(details);
    expect(draft).toContain('《旧版退款约定》的第 2 版');
    expect(draft).toContain('/knowledge?item=kb_1&version=2');
    expect(draft).toContain('暂时不要修改知识');
    expect(draft).not.toContain(details.version.body_markdown);
  });
});
