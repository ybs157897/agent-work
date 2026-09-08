import {
  Background,
  Controls,
  Handle,
  MiniMap,
  Position,
  ReactFlow,
  applyNodeChanges,
  useEdgesState,
  useNodesState,
  type Edge,
  type Node,
  type NodeChange,
  type NodeProps,
  type Viewport,
} from '@xyflow/react';
import { AlertCircle, ChevronRight, FileText, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  listKnowledgeRelations,
  type KnowledgeItem,
  type KnowledgeRelation,
} from '../../api/knowledge';
import { Button, EmptyState, cx } from '../ui';
import '@xyflow/react/dist/style.css';

const MAX_GRAPH_ITEMS = 60;
const RELATION_CONCURRENCY = 4;

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

const GRAPH_ARIA_LABELS = {
  'controls.ariaLabel': '知识画布控件',
  'controls.zoomIn.ariaLabel': '放大画布',
  'controls.zoomOut.ariaLabel': '缩小画布',
  'controls.fitView.ariaLabel': '适应视图',
  'controls.interactive.ariaLabel': '切换画布交互',
  'minimap.ariaLabel': '知识文档缩略图',
} as const;

export type KnowledgeGraphNodeData = {
  item: KnowledgeItem;
  selected: boolean;
  onOpen: (item: KnowledgeItem) => void;
};

export type KnowledgeGraphNode = Node<KnowledgeGraphNodeData, 'knowledge'>;

export interface KnowledgeGraphData {
  nodes: KnowledgeGraphNode[];
  edges: Edge[];
}

export interface KnowledgeGraphRelationsResult {
  relations: KnowledgeRelation[];
  failures: number;
}

export type KnowledgeRelationLoader = (
  workspaceId: string,
  itemId: string,
  direction: 'both',
  agentId?: string,
) => Promise<{ items: KnowledgeRelation[] }>;

interface KnowledgeGraphViewport extends Viewport {
  x: number;
  y: number;
  zoom: number;
}

function readKnowledgeGraphViewport(key: string): KnowledgeGraphViewport | null {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.localStorage.getItem(`${key}:graph-viewport`);
    if (!raw) return null;
    const value = JSON.parse(raw) as Partial<KnowledgeGraphViewport>;
    if (![value.x, value.y, value.zoom].every((entry) => typeof entry === 'number' && Number.isFinite(entry))) return null;
    return { x: value.x!, y: value.y!, zoom: value.zoom! };
  } catch {
    return null;
  }
}

function writeKnowledgeGraphViewport(key: string, viewport: Viewport): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(`${key}:graph-viewport`, JSON.stringify(viewport));
  } catch {
    // Graph position is a convenience; blocked storage must not affect reading.
  }
}

function readKnowledgeGraphPositions(key: string): Record<string, { x: number; y: number }> {
  if (typeof window === 'undefined') return {};
  try {
    const raw = window.localStorage.getItem(`${key}:graph-nodes`);
    if (!raw) return {};
    const value = JSON.parse(raw) as Record<string, { x?: number; y?: number }>;
    return Object.fromEntries(Object.entries(value).flatMap(([id, position]) => (
      typeof position?.x === 'number' && Number.isFinite(position.x) && typeof position.y === 'number' && Number.isFinite(position.y)
        ? [[id, { x: position.x, y: position.y }]]
        : []
    )));
  } catch {
    return {};
  }
}

function writeKnowledgeGraphPositions(key: string, nodes: KnowledgeGraphNode[]): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(`${key}:graph-nodes`, JSON.stringify(Object.fromEntries(nodes.map((node) => [node.id, node.position]))));
  } catch {
    // Graph position is a convenience; blocked storage must not affect reading.
  }
}

/**
 * Fetch relation lists with a hard four-request ceiling. The API response is
 * then intersected with the loaded node set so inaccessible or out-of-window
 * endpoints cannot become graph edges.
 */
export async function loadVisibleKnowledgeRelations(
  workspaceId: string,
  items: KnowledgeItem[],
  requesterAgentId?: string,
  load: KnowledgeRelationLoader = listKnowledgeRelations,
): Promise<KnowledgeGraphRelationsResult> {
  const relations: KnowledgeRelation[] = [];
  const nodeIds = new Set(items.slice(0, MAX_GRAPH_ITEMS).map((item) => item.id));
  let cursor = 0;
  let failures = 0;
  const workers = Math.min(RELATION_CONCURRENCY, items.length, MAX_GRAPH_ITEMS);

  async function worker(): Promise<void> {
    while (true) {
      const index = cursor++;
      if (index >= items.length || index >= MAX_GRAPH_ITEMS) return;
      try {
        const result = await load(workspaceId, items[index]!.id, 'both', requesterAgentId);
        result.items.forEach((relation) => {
          if (!nodeIds.has(relation.from_item_id) || !nodeIds.has(relation.to_item_id)) return;
          if (!relations.some((existing) => existing.id === relation.id)) relations.push(relation);
        });
      } catch {
        failures += 1;
      }
    }
  }

  await Promise.all(Array.from({ length: workers }, () => worker()));
  return { relations, failures };
}

export function buildKnowledgeGraph(items: KnowledgeItem[], relations: KnowledgeRelation[], selectedItemId: string | null, onOpen: (item: KnowledgeItem) => void): KnowledgeGraphData {
  const visibleItems = items.slice(0, MAX_GRAPH_ITEMS);
  const itemIds = new Set(visibleItems.map((item) => item.id));
  const columnCount = visibleItems.length <= 6 ? 2 : 4;
  const columnGap = columnCount === 2 ? 248 : 268;
  const nodes = visibleItems.map<KnowledgeGraphNode>((item, index) => ({
    id: item.id,
    type: 'knowledge',
    position: { x: (index % columnCount) * columnGap + 32, y: Math.floor(index / columnCount) * 152 + 28 },
    data: { item, selected: item.id === selectedItemId, onOpen },
  }));
  const edges = relations
    .filter((relation) => itemIds.has(relation.from_item_id) && itemIds.has(relation.to_item_id))
    .map<Edge>((relation) => ({
      id: relation.id,
      source: relation.from_item_id,
      target: relation.to_item_id,
      label: RELATION_LABELS[relation.kind] ?? relation.kind,
      type: 'smoothstep',
      className: 'knowledge-graph-edge',
      ariaLabel: `${relation.from_item_id} ${RELATION_LABELS[relation.kind] ?? relation.kind} ${relation.to_item_id}`,
    }));
  return { nodes, edges };
}

function KnowledgeNode({ data }: NodeProps<KnowledgeGraphNode>) {
  return (
    <div
      className={cx('knowledge-graph-node', data.selected && 'is-selected')}
      role="button"
      tabIndex={0}
      aria-label={`打开文档：${data.item.title}`}
      onClick={() => data.onOpen(data.item)}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          data.onOpen(data.item);
        }
      }}
    >
      <Handle type="target" position={Position.Left} aria-label="关系输入" />
      <div className="knowledge-graph-node-topline">
        <FileText className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span>v{data.item.current_version}</span>
      </div>
      <strong className="knowledge-graph-node-title">{data.item.title}</strong>
      <span className="knowledge-graph-node-summary">{data.item.summary || '暂无摘要'}</span>
      <span className="knowledge-graph-node-open">打开文档 <ChevronRight className="h-3 w-3" aria-hidden="true" /></span>
      <Handle type="source" position={Position.Right} aria-label="关系输出" />
    </div>
  );
}

const nodeTypes = { knowledge: KnowledgeNode };

export function reconcileKnowledgeGraphNodes(current: KnowledgeGraphNode[], next: KnowledgeGraphNode[]): KnowledgeGraphNode[] {
  const positions = new Map(current.map((node) => [node.id, node.position]));
  return next.map((node) => ({ ...node, position: positions.get(node.id) ?? node.position }));
}

export function KnowledgeGraph({
  items,
  workspaceId,
  viewportStorageKey,
  requesterAgentId,
  selectedItemId,
  onSelectItem,
}: {
  items: KnowledgeItem[];
  workspaceId: string;
  viewportStorageKey: string;
  requesterAgentId?: string;
  selectedItemId: string | null;
  onSelectItem: (item: KnowledgeItem) => void;
}) {
  const [relationState, setRelationState] = useState<{ kind: 'loading' } | { kind: 'ready'; value: KnowledgeGraphRelationsResult } | { kind: 'error'; message: string }>({ kind: 'loading' });
  const requestId = useRef(0);
  const graphItems = useMemo(() => items.slice(0, MAX_GRAPH_ITEMS), [items]);
  const savedViewport = useMemo(() => readKnowledgeGraphViewport(viewportStorageKey), [viewportStorageKey]);
  const savedPositions = useMemo(() => readKnowledgeGraphPositions(viewportStorageKey), [viewportStorageKey]);
  const handleMoveEnd = useCallback((_event: unknown, viewport: Viewport) => writeKnowledgeGraphViewport(viewportStorageKey, viewport), [viewportStorageKey]);

  const loadRelations = useCallback(() => {
    const current = ++requestId.current;
    setRelationState({ kind: 'loading' });
    void loadVisibleKnowledgeRelations(workspaceId, graphItems, requesterAgentId).then((value) => {
      if (current === requestId.current) setRelationState({ kind: 'ready', value });
    }).catch((error: unknown) => {
      if (current !== requestId.current) return;
      setRelationState({ kind: 'error', message: error instanceof Error && error.message ? error.message : '关联读取失败，请重试。' });
    });
  }, [graphItems, requesterAgentId, workspaceId]);

  useEffect(() => {
    loadRelations();
    return () => { requestId.current += 1; };
  }, [loadRelations]);

  const graph = useMemo(() => {
    const base = buildKnowledgeGraph(graphItems, relationState.kind === 'ready' ? relationState.value.relations : [], selectedItemId, onSelectItem);
    return {
      nodes: base.nodes.map((node) => savedPositions[node.id] ? { ...node, position: savedPositions[node.id] } : node),
      edges: base.edges,
    };
  }, [graphItems, onSelectItem, relationState, savedPositions, selectedItemId]);
  const [nodes, setNodes] = useNodesState(graph.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(graph.edges);

  const handleNodesChange = useCallback((changes: NodeChange<KnowledgeGraphNode>[]) => {
    setNodes((current) => {
      const next = applyNodeChanges(changes, current) as KnowledgeGraphNode[];
      writeKnowledgeGraphPositions(viewportStorageKey, next);
      return next;
    });
  }, [setNodes, viewportStorageKey]);

  useEffect(() => setNodes((current) => reconcileKnowledgeGraphNodes(current, graph.nodes)), [graph.nodes, setNodes]);
  useEffect(() => setEdges(graph.edges), [graph.edges, setEdges]);

  if (graphItems.length === 0) {
    return <EmptyState icon={<FileText className="h-5 w-5" aria-hidden="true" />} title="还没有可展示的文档" description="已发布知识出现后，全景关系会在这里建立。" />;
  }

  return (
    <div className="knowledge-graph" data-testid="knowledge-graph" aria-label="知识全景画布">
      <div className="knowledge-graph-toolbar">
        <div className="min-w-0"><p className="text-body font-medium text-text-primary">知识全景</p><p className="text-caption text-text-tertiary">{graphItems.length}{items.length > MAX_GRAPH_ITEMS ? ` / ${items.length}` : ''} 份文档 · 仅显示真实关联</p></div>
        <div className="knowledge-graph-toolbar-actions">
          {relationState.kind === 'ready' && relationState.value.failures > 0 ? <span className="knowledge-graph-failure-count" role="alert"><AlertCircle className="h-3.5 w-3.5" aria-hidden="true" />{relationState.value.failures} 个关联读取失败</span> : null}
          <Button type="button" size="sm" onClick={loadRelations} disabled={relationState.kind === 'loading'}><RefreshCw className={cx('h-3.5 w-3.5', relationState.kind === 'loading' && 'animate-spin motion-reduce:animate-none')} aria-hidden="true" />刷新关联</Button>
        </div>
      </div>
      {relationState.kind === 'error' ? <div className="knowledge-graph-error" role="alert"><AlertCircle className="h-4 w-4 shrink-0 text-status-error" aria-hidden="true" /><span className="min-w-0 flex-1">{relationState.message}</span><Button type="button" size="sm" onClick={loadRelations}>重试</Button></div> : null}
      <div className="knowledge-graph-flow">
        <ReactFlow nodes={nodes} edges={edges} nodeTypes={nodeTypes} onNodesChange={handleNodesChange} onEdgesChange={onEdgesChange} onMoveEnd={handleMoveEnd} defaultViewport={savedViewport ?? { x: 0, y: 0, zoom: 1 }} fitView={!savedViewport} fitViewOptions={{ padding: 0.2 }} minZoom={0.35} maxZoom={1.6} nodesDraggable nodesConnectable={false} panOnDrag zoomOnScroll aria-label="知识文档关系图" ariaLabelConfig={GRAPH_ARIA_LABELS}>
          <Background gap={24} size={1} />
          <Controls showInteractive={false} position="bottom-left" aria-label="知识画布控件" />
          <MiniMap pannable zoomable ariaLabel="知识文档缩略图" nodeColor="hsl(var(--color-brand-primary))" maskColor="hsl(var(--color-surface-base) / 0.72)" />
        </ReactFlow>
      </div>
      {items.length > MAX_GRAPH_ITEMS ? <p className="knowledge-canvas-partial-notice" role="status">全景只加载前 {MAX_GRAPH_ITEMS} 份文档，拖动和缩放布局不会修改知识正文。</p> : null}
    </div>
  );
}
