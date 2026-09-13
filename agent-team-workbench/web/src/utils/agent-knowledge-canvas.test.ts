import { describe, expect, it } from 'vitest';
import {
  buildCanvasMessage,
  parseCanvasMessage,
  referenceBelongsTo,
  restoreCanvasComposer,
  type KnowledgeCanvasReference,
} from './agent-knowledge-canvas';

const reference: KnowledgeCanvasReference = {
  workspaceId: 'ws_1',
  agentId: 'a_pm',
  documentId: 'doc_1',
  version: 2,
  releaseId: 'rel_7',
  title: '退款\n规则',
  quote: '退款期限为 14 天。\n保留申请记录。',
  heading: '退款窗口',
};

describe('产品知识画布的引用协议', () => {
  it('只接受当前工作空间、当前 Agent 且带完整定位事实的引用', () => {
    expect(referenceBelongsTo(reference, 'ws_1', 'a_pm')).toBe(true);
    expect(referenceBelongsTo(reference, 'ws_2', 'a_pm')).toBe(false);
    expect(referenceBelongsTo(reference, 'ws_1', 'a_other')).toBe(false);
    expect(referenceBelongsTo({ ...reference, version: 0 }, 'ws_1', 'a_pm')).toBe(false);
    expect(referenceBelongsTo({ ...reference, documentId: '' }, 'ws_1', 'a_pm')).toBe(false);
    expect(referenceBelongsTo({ ...reference, releaseId: '' }, 'ws_1', 'a_pm')).toBe(false);
  });

  it('把摘录与版本事实折进消息尾部，普通问题原样返回', () => {
    const message = buildCanvasMessage('请解释这个规则。', reference);
    expect(message.startsWith('请解释这个规则。')).toBe(true);
    expect(message).toContain('<atw-knowledge-reference-v1>');
    expect(parseCanvasMessage(message)).toEqual({
      question: '请解释这个规则。',
      reference: { ...reference, title: '退款 规则' },
    });
    expect(message).not.toContain('/knowledge?');
    expect(buildCanvasMessage(' 普通问题 ', null)).toBe('普通问题');
  });

  it('长摘录有界截断但仍保留文档与版本标识', () => {
    const message = buildCanvasMessage('评审', { ...reference, quote: '文'.repeat(9000) });
    expect(message).toContain('摘录到此为止');
    expect(parseCanvasMessage(message)?.reference.documentId).toBe('doc_1');
    expect(parseCanvasMessage(message)?.reference.releaseId).toBe('rel_7');
    expect(message.length).toBeLessThan(6400);
  });

  it('发送失败时恢复可移除的引用，且不覆盖更新后的选区', () => {
    const failed = { draft: '请解释规则', reference };
    expect(restoreCanvasComposer({ draft: '', reference: null }, failed)).toEqual(failed);
    const newer = { ...reference, documentId: 'doc_2', title: '另一份文档', quote: '新的选区' };
    const result = restoreCanvasComposer({ draft: '还有一个问题', reference: newer }, failed);
    expect(result.reference).toBe(newer);
    expect(result.draft).toContain('"documentId":"doc_1"');
    expect(result.draft).toContain('退款期限为 14 天。');
    expect(result.draft.endsWith('还有一个问题')).toBe(true);
  });

  it('不把普通行文或残缺标记当成引用卡片', () => {
    expect(parseCanvasMessage('引用文档：《规则》 · 第 2 版')).toBeNull();
    expect(parseCanvasMessage('问题\n\n<atw-knowledge-reference-v1>\n{}\n</atw-knowledge-reference-v1>')).toBeNull();
    expect(parseCanvasMessage(buildCanvasMessage('问题', reference) + '\n用户补充')).toBeNull();
    expect(parseCanvasMessage(buildCanvasMessage('问题', { ...reference, documentId: '' }))).toBeNull();
  });
});
