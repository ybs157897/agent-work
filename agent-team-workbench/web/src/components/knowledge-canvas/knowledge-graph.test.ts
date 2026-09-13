/**
 * `buildKnowledgeGraph` 的数据投影单测：只断言传入快照 → 节点/边的结果，
 * 不渲染 React（该仓 vitest 的 environment 是 node，没有 jsdom）。
 */
import { describe, expect, it, vi } from 'vitest';
import type { DocumentSummary, Endpoint, GraphResponse, Relation } from '../../api/knowledge-library';
import { MAX_KNOWLEDGE_GRAPH_NODES, buildKnowledgeGraph } from './knowledge-graph';

type GraphEntity = GraphResponse['entities'][number];

function makeDocument(id: string, overrides: Partial<DocumentSummary> = {}): DocumentSummary {
  return {
    id,
    path: `docs/${id}.md`,
    kind: 'spec',
    title: `文档 ${id}`,
    summary: `摘要 ${id}`,
    domains: [],
    status: 'active',
    renamed_from: '',
    version: 1,
    updated_at: '2026-09-12T00:00:00Z',
    ...overrides,
  };
}

function makeEntity(id: string, overrides: Partial<GraphEntity> = {}): GraphEntity {
  return { id, kind: 'service', namespace: 'svc', canonical_name: `实体 ${id}`, aliases: [], ...overrides };
}

function makeRelation(id: string, from: Endpoint, to: Endpoint, overrides: Partial<Relation> = {}): Relation {
  return {
    id,
    document_id: 'doc-a',
    from,
    predicate: 'calls',
    to,
    to_resolved: true,
    to_raw: to.id,
    perspective: 'normative',
    basis: 'source_statement',
    condition: '',
    evidence: [],
    ...overrides,
  };
}

function makeGraph(overrides: Partial<GraphResponse> = {}): GraphResponse {
  return { release_id: 'release-1', entities: [], relations: [], documents: [], ...overrides };
}

describe('buildKnowledgeGraph 节点投影', () => {
  it('没有快照时返回空投影', () => {
    expect(buildKnowledgeGraph(null, null, () => {})).toEqual({ nodes: [], edges: [] });
  });

  it('文档节点与实体节点都产出，id 带前缀且不与文档 id 撞车', () => {
    const graph = makeGraph({
      documents: [makeDocument('doc-a'), makeDocument('doc-b')],
      // 实体 id 与文档 id 故意相同：两类节点不能互相覆盖。
      entities: [makeEntity('doc-a'), makeEntity('svc-a', { document_id: 'doc-b' })],
    });

    const first = buildKnowledgeGraph(graph, null, () => {});
    const second = buildKnowledgeGraph(graph, null, () => {});

    expect(first.nodes.map((node) => node.id)).toEqual([
      'entity:doc-a',
      'entity:svc-a',
      'document:doc-a',
      'document:doc-b',
    ]);
    expect(new Set(first.nodes.map((node) => node.id)).size).toBe(first.nodes.length);
    expect(second.nodes.map((node) => node.id)).toEqual(first.nodes.map((node) => node.id));
    expect(second.nodes.map((node) => node.position)).toEqual(first.nodes.map((node) => node.position));
    first.nodes.forEach((node) => {
      expect(Number.isFinite(node.position.x) && Number.isFinite(node.position.y)).toBe(true);
    });

    expect(first.nodes.find((node) => node.id === 'document:doc-a')?.data).toMatchObject({
      kind: 'document',
      label: '文档 doc-a',
      subtitle: '摘要 doc-a',
      documentId: 'doc-a',
    });
    const entityWithoutOwner = first.nodes.find((node) => node.id === 'entity:doc-a')?.data;
    expect(entityWithoutOwner).toMatchObject({ kind: 'entity', label: '实体 doc-a', subtitle: 'svc' });
    expect(entityWithoutOwner?.documentId).toBeUndefined();
    expect(first.nodes.find((node) => node.id === 'entity:svc-a')?.data.documentId).toBe('doc-b');
    expect(first.edges).toEqual([]);
  });

  it('实体分区排在文档分区之前，两个分区不重叠', () => {
    const graph = makeGraph({
      documents: [makeDocument('doc-a'), makeDocument('doc-b')],
      entities: [makeEntity('svc-a'), makeEntity('svc-b')],
    });

    const { nodes } = buildKnowledgeGraph(graph, null, () => {});
    const entityBottom = Math.max(...nodes.filter((node) => node.data.kind === 'entity').map((node) => node.position.y));
    const documentTop = Math.min(...nodes.filter((node) => node.data.kind === 'document').map((node) => node.position.y));
    expect(entityBottom).toBeLessThan(documentTop);
  });

  it('重复的文档/实体 id 只画一个节点', () => {
    const graph = makeGraph({
      documents: [makeDocument('doc-a'), makeDocument('doc-a', { title: '另一份' })],
      entities: [makeEntity('svc-a'), makeEntity('svc-a')],
    });

    const { nodes } = buildKnowledgeGraph(graph, null, () => {});
    expect(nodes.map((node) => node.id)).toEqual(['entity:svc-a', 'document:doc-a']);
  });
});

describe('buildKnowledgeGraph 边投影', () => {
  it('只画两端都能落到已渲染节点的关系，assertion 与未知端点被丢弃', () => {
    const graph = makeGraph({
      documents: [makeDocument('doc-a'), makeDocument('doc-b')],
      entities: [makeEntity('svc-a'), makeEntity('svc-b')],
      relations: [
        makeRelation('rel-entity-entity', { kind: 'entity', id: 'svc-a' }, { kind: 'entity', id: 'svc-b' }),
        makeRelation('rel-document-entity', { kind: 'document', id: 'doc-a' }, { kind: 'entity', id: 'svc-a' }),
        makeRelation('rel-to-assertion', { kind: 'entity', id: 'svc-a' }, { kind: 'assertion', id: 'assert-1' }),
        makeRelation('rel-from-assertion', { kind: 'assertion', id: 'assert-1' }, { kind: 'document', id: 'doc-a' }),
        makeRelation('rel-unknown-entity', { kind: 'entity', id: 'svc-a' }, { kind: 'entity', id: 'svc-missing' }),
        makeRelation('rel-unknown-document', { kind: 'document', id: 'doc-missing' }, { kind: 'entity', id: 'svc-a' }),
      ],
    });

    const { nodes, edges } = buildKnowledgeGraph(graph, null, () => {});

    expect(edges.map((edge) => edge.id)).toEqual(['rel-entity-entity', 'rel-document-entity']);
    expect(edges.map((edge) => [edge.source, edge.target])).toEqual([
      ['entity:svc-a', 'entity:svc-b'],
      ['document:doc-a', 'entity:svc-a'],
    ]);
    const nodeIds = new Set(nodes.map((node) => node.id));
    edges.forEach((edge) => {
      expect(nodeIds.has(edge.source)).toBe(true);
      expect(nodeIds.has(edge.target)).toBe(true);
    });
    // assertion 端点不产生占位节点。
    expect(nodes.some((node) => node.id.includes('assert-1'))).toBe(false);
    expect(nodes.some((node) => node.id.includes('doc-missing'))).toBe(false);
  });

  it('边 label 用受控词表的中文名，表外 predicate 原样显示', () => {
    const graph = makeGraph({
      documents: [],
      entities: [makeEntity('svc-a'), makeEntity('svc-b')],
      relations: [
        makeRelation('rel-known', { kind: 'entity', id: 'svc-a' }, { kind: 'entity', id: 'svc-b' }, { predicate: 'depends_on' }),
        makeRelation('rel-unknown', { kind: 'entity', id: 'svc-b' }, { kind: 'entity', id: 'svc-a' }, { predicate: 'invented_by' }),
      ],
    });

    const { edges } = buildKnowledgeGraph(graph, null, () => {});
    expect(edges.map((edge) => edge.label)).toEqual(['依赖', 'invented_by']);
    expect(edges[0]?.ariaLabel).toBe('实体 svc-a 依赖 实体 svc-b');
  });

  it('重复的 relation id 只画第一条', () => {
    const graph = makeGraph({
      entities: [makeEntity('svc-a'), makeEntity('svc-b')],
      relations: [
        makeRelation('rel-dup', { kind: 'entity', id: 'svc-a' }, { kind: 'entity', id: 'svc-b' }),
        makeRelation('rel-dup', { kind: 'entity', id: 'svc-b' }, { kind: 'entity', id: 'svc-a' }),
      ],
    });

    const { edges } = buildKnowledgeGraph(graph, null, () => {});
    expect(edges.map((edge) => edge.id)).toEqual(['rel-dup']);
    expect(edges[0]?.source).toBe('entity:svc-a');
  });
});

describe('buildKnowledgeGraph 选中态与打开回调', () => {
  it('selectedDocumentId 只命中对应的文档节点', () => {
    const graph = makeGraph({
      documents: [makeDocument('doc-a'), makeDocument('doc-b')],
      entities: [makeEntity('svc-a', { document_id: 'doc-b' })],
    });

    const { nodes } = buildKnowledgeGraph(graph, 'doc-b', () => {});
    expect(nodes.filter((node) => node.data.selected).map((node) => node.id)).toEqual(['document:doc-b']);
    expect(nodes.find((node) => node.id === 'entity:svc-a')?.data.selected).toBe(false);

    const cleared = buildKnowledgeGraph(graph, null, () => {});
    expect(cleared.nodes.some((node) => node.data.selected)).toBe(false);
  });

  it('文档节点与带 document_id 的实体节点指向同一回调，无归属实体的 onOpen 不调用回调', () => {
    const onOpenDocument = vi.fn();
    const graph = makeGraph({
      documents: [makeDocument('doc-a')],
      entities: [makeEntity('svc-owned', { document_id: 'doc-b' }), makeEntity('svc-floating')],
    });

    const { nodes } = buildKnowledgeGraph(graph, null, onOpenDocument);
    const openDocument = (id: string) => nodes.find((node) => node.id === id)?.data;

    expect(openDocument('entity:svc-floating')?.documentId).toBeUndefined();

    openDocument('document:doc-a')?.onOpen();
    expect(onOpenDocument).toHaveBeenLastCalledWith('doc-a');

    openDocument('entity:svc-owned')?.onOpen();
    expect(onOpenDocument).toHaveBeenLastCalledWith('doc-b');

    openDocument('entity:svc-floating')?.onOpen();
    expect(onOpenDocument).toHaveBeenCalledTimes(2);
  });
});

describe('buildKnowledgeGraph 节点总量裁剪', () => {
  it('节点数超过上限时裁到上限，文档优先占名额', () => {
    const documents = Array.from({ length: 80 }, (_, index) => makeDocument(`doc-${index}`));
    const entities = Array.from({ length: 30 }, (_, index) => makeEntity(`svc-${index}`));

    const { nodes } = buildKnowledgeGraph(makeGraph({ documents, entities }), null, () => {});

    expect(nodes).toHaveLength(MAX_KNOWLEDGE_GRAPH_NODES);
    expect(nodes.every((node) => node.data.kind === 'document')).toBe(true);
    expect(nodes.map((node) => node.id)).toEqual(documents.slice(0, MAX_KNOWLEDGE_GRAPH_NODES).map((doc) => `document:${doc.id}`));
  });

  it('名额有余量时实体补足，被裁掉端点的关系一并丢弃', () => {
    const documents = Array.from({ length: 25 }, (_, index) => makeDocument(`doc-${index}`));
    const entities = Array.from({ length: 50 }, (_, index) => makeEntity(`svc-${index}`));
    const graph = makeGraph({
      documents,
      entities,
      relations: [
        makeRelation('rel-kept', { kind: 'document', id: 'doc-0' }, { kind: 'entity', id: 'svc-0' }),
        makeRelation('rel-cut', { kind: 'document', id: 'doc-0' }, { kind: 'entity', id: 'svc-49' }),
      ],
    });

    const { nodes, edges } = buildKnowledgeGraph(graph, null, () => {});

    expect(nodes).toHaveLength(MAX_KNOWLEDGE_GRAPH_NODES);
    expect(nodes.filter((node) => node.data.kind === 'document')).toHaveLength(25);
    expect(nodes.filter((node) => node.data.kind === 'entity')).toHaveLength(MAX_KNOWLEDGE_GRAPH_NODES - 25);
    expect(nodes.some((node) => node.id === 'entity:svc-49')).toBe(false);
    expect(edges.map((edge) => edge.id)).toEqual(['rel-kept']);
  });
});
