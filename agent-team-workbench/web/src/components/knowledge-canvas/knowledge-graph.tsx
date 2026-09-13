/**
 * 知识全景画布：把资料库发布快照（`GraphResponse`）投影成 React Flow 图。
 *
 * 只画服务端事实。节点来自 `documents` / `entities` 两个数组；边只取 `relations`
 * 里两端都能落到已渲染节点的条目——指向 `assertion` 或任何未渲染端点的关系一律
 * 丢弃，不为它们补占位节点，也不为未知 predicate 编造语义。
 *
 * 布局是确定性网格（不做力导向，渲染结果可断言）：实体分区在上、文档分区在下，
 * 分区内按服务端顺序每行 4 列，两个分区之间留一段空白。React Flow 的视口和拖拽后
 * 的节点位置按 `viewportStorageKey` 存 localStorage，storage 不可用只影响记忆，
 * 不影响渲染。
 *
 * 上限裁的是**节点总量**：超过 `MAX_KNOWLEDGE_GRAPH_NODES` 时只取前 N 个，文档优先
 * 占名额（文档是阅读入口，也是选中态锚点），实体补足剩余名额。
 */
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
import { AlertCircle, Boxes, ChevronRight, FileText, LoaderCircle, RefreshCw } from 'lucide-react';
import { useCallback, useEffect, useMemo } from 'react';
import type { DocumentSummary, Endpoint, GraphResponse } from '../../api/knowledge-library';
import { Button, EmptyState, cx } from '../ui';
import '@xyflow/react/dist/style.css';

/** 画布上的节点总量上限（不是文档数上限）。 */
export const MAX_KNOWLEDGE_GRAPH_NODES = 60;

/** 网格几何：位置只由分区内序号决定，同一份快照每次得到相同的坐标。 */
const GRID_COLUMNS = 4;
const COLUMN_GAP = 268;
const ROW_GAP = 152;
const ORIGIN_X = 32;
const ORIGIN_Y = 28;
/** 实体分区与文档分区之间的垂直空白。 */
const PARTITION_GAP = 96;

/**
 * 受控词表 predicate 的中文展示名，取自服务端 `RelationPredicates`
 * （agent-team-workbench/internal/knowledgelib/record.go）。表外的 predicate
 * 原样显示字符串，不做猜测。
 */
const PREDICATE_LABELS: Record<string, string> = {
  calls: '调用',
  depends_on: '依赖',
  publishes: '发布',
  consumes: '消费',
  reads: '读取',
  writes: '写入',
  implements: '实现',
  contains: '包含',
  deviates_from: '偏离',
  contradicts: '冲突',
  supersedes: '取代',
  impacts: '影响',
  triggers: '触发',
  shares_data: '共享数据',
};

const GRAPH_ARIA_LABELS = {
  'controls.ariaLabel': '知识画布控件',
  'controls.zoomIn.ariaLabel': '放大画布',
  'controls.zoomOut.ariaLabel': '缩小画布',
  'controls.fitView.ariaLabel': '适应视图',
  'controls.interactive.ariaLabel': '切换画布交互',
  'minimap.ariaLabel': '知识图谱缩略图',
} as const;

/**
 * 节点数据。`extends Record<string, unknown>` 只为满足 `@xyflow/react` 的
 * `Node<NodeData extends Record<string, unknown>>` 约束：接口不会像类型别名那样
 * 自动获得隐式索引签名。
 */
export interface KnowledgeGraphNodeData extends Record<string, unknown> {
  kind: 'entity' | 'document';
  label: string;
  subtitle?: string;
  documentId?: string;
  selected: boolean;
  onOpen: () => void;
}

export type KnowledgeGraphNode = Node<KnowledgeGraphNodeData, 'knowledge'>;

export interface KnowledgeGraphProps {
  /** null = 父组件还没拿到发布快照。 */
  graph: GraphResponse | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  selectedDocumentId: string | null;
  onOpenDocument: (documentId: string) => void;
  /** 视口与拖拽后节点位置的 localStorage 键前缀。 */
  viewportStorageKey: string;
}

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
    // 视口位置只是便利记忆；storage 被禁用不应影响阅读。
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
    // 节点位置只是便利记忆；storage 被禁用不应影响阅读。
  }
}

const documentNodeId = (id: string): string => `document:${id}`;
const entityNodeId = (id: string): string => `entity:${id}`;

/** 解析不到所属文档的实体：保留同一签名的回调，但点开不指向任何目标。 */
const NO_OPEN = (): void => undefined;

function dedupeById<T>(items: readonly T[], id: (item: T) => string): T[] {
  const seen = new Set<string>();
  return items.filter((item) => {
    const key = id(item);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

/** 快照里的文档与实体各去重一次：节点 id 是 `<kind>:<id>`，重复条目只画一个。 */
function collectKnowledgeGraphSources(graph: GraphResponse): { documents: DocumentSummary[]; entities: GraphResponse['entities'] } {
  return {
    documents: dedupeById(graph.documents, (doc) => doc.id),
    entities: dedupeById(graph.entities, (entity) => entity.id),
  };
}

/** 把 `from` / `to` 端点映射到节点 id；assertion 与未知 kind 没有对应节点。 */
function endpointNodeId(endpoint: Endpoint): string | null {
  if (endpoint.kind === 'entity') return entityNodeId(endpoint.id);
  if (endpoint.kind === 'document') return documentNodeId(endpoint.id);
  return null;
}

/** 分区内按序号取网格坐标；`offsetY` 让文档分区整体落在实体分区之后。 */
function gridPosition(index: number, offsetY: number): { x: number; y: number } {
  return {
    x: (index % GRID_COLUMNS) * COLUMN_GAP + ORIGIN_X,
    y: Math.floor(index / GRID_COLUMNS) * ROW_GAP + ORIGIN_Y + offsetY,
  };
}

/**
 * 纯数据投影：快照 → 画布节点与边。测试只测这个函数，不测 React 渲染。
 *
 * 边的取舍：`from` / `to` 中任一端点解析不到已渲染节点（assertion、未知 kind、
 * 被裁掉或快照里不存在的 id）就丢弃；边 label 用受控词表的中文名，表外显示原始
 * predicate。重复的 relation id 只画第一条。
 */
export function buildKnowledgeGraph(
  graph: GraphResponse | null,
  selectedDocumentId: string | null,
  onOpenDocument: (documentId: string) => void,
): { nodes: KnowledgeGraphNode[]; edges: Edge[] } {
  if (!graph) return { nodes: [], edges: [] };

  const { documents, entities } = collectKnowledgeGraphSources(graph);
  const visibleDocuments = documents.slice(0, MAX_KNOWLEDGE_GRAPH_NODES);
  const visibleEntities = entities.slice(0, Math.max(0, MAX_KNOWLEDGE_GRAPH_NODES - visibleDocuments.length));
  const documentOffsetY = visibleEntities.length > 0
    ? Math.ceil(visibleEntities.length / GRID_COLUMNS) * ROW_GAP + PARTITION_GAP
    : 0;

  const entityNodes = visibleEntities.map<KnowledgeGraphNode>((entity, index) => {
    const documentId = entity.document_id || undefined;
    return {
      id: entityNodeId(entity.id),
      type: 'knowledge',
      position: gridPosition(index, 0),
      data: {
        kind: 'entity',
        label: entity.canonical_name || entity.id,
        subtitle: entity.namespace || entity.kind || undefined,
        documentId,
        selected: false,
        onOpen: documentId ? () => onOpenDocument(documentId) : NO_OPEN,
      },
    };
  });

  const documentNodes = visibleDocuments.map<KnowledgeGraphNode>((doc, index) => ({
    id: documentNodeId(doc.id),
    type: 'knowledge',
    position: gridPosition(index, documentOffsetY),
    data: {
      kind: 'document',
      label: doc.title || doc.path || doc.id,
      subtitle: doc.summary || doc.path || undefined,
      documentId: doc.id,
      selected: doc.id === selectedDocumentId,
      onOpen: () => onOpenDocument(doc.id),
    },
  }));

  const nodes = [...entityNodes, ...documentNodes];
  const nodeIds = new Set(nodes.map((node) => node.id));
  const labelsByNodeId = new Map(nodes.map((node) => [node.id, node.data.label]));
  const seenEdgeIds = new Set<string>();
  const edges: Edge[] = [];

  graph.relations.forEach((relation) => {
    const source = endpointNodeId(relation.from);
    const target = endpointNodeId(relation.to);
    if (!source || !target) return;
    if (!nodeIds.has(source) || !nodeIds.has(target)) return;
    if (seenEdgeIds.has(relation.id)) return;
    seenEdgeIds.add(relation.id);
    const label = PREDICATE_LABELS[relation.predicate] ?? relation.predicate;
    edges.push({
      id: relation.id,
      source,
      target,
      type: 'smoothstep',
      label,
      className: 'knowledge-graph-edge',
      ariaLabel: `${labelsByNodeId.get(source) ?? source} ${label} ${labelsByNodeId.get(target) ?? target}`,
    });
  });

  return { nodes, edges };
}

/**
 * 把已拖拽的位置覆盖回新投影：节点集合变化时保留同 id 节点的位置，其余用网格坐标。
 * 无变化时逐个返回原对象，避免父组件重渲染时反复写 state。
 */
export function reconcileKnowledgeGraphNodes(current: KnowledgeGraphNode[], next: KnowledgeGraphNode[]): KnowledgeGraphNode[] {
  const positions = new Map(current.map((node) => [node.id, node.position]));
  return next.map((node) => {
    const position = positions.get(node.id);
    if (!position) return node;
    return position.x === node.position.x && position.y === node.position.y ? node : { ...node, position };
  });
}

function isSameNodeList(current: KnowledgeGraphNode[], next: KnowledgeGraphNode[]): boolean {
  return current.length === next.length && current.every((node, index) => node === next[index]);
}

function KnowledgeNode({ data }: NodeProps<KnowledgeGraphNode>) {
  const openable = Boolean(data.documentId);
  const isDocument = data.kind === 'document';
  const openLabel = isDocument ? '打开文档' : '打开所属文档';
  const handleOpen = () => {
    if (openable) data.onOpen();
  };

  return (
    <div
      className={cx('knowledge-graph-node', data.selected && 'is-selected')}
      role={openable ? 'button' : undefined}
      tabIndex={openable ? 0 : undefined}
      aria-label={openable ? `${openLabel}：${data.label}` : `实体：${data.label}`}
      onClick={handleOpen}
      onKeyDown={(event) => {
        if (!openable) return;
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          data.onOpen();
        }
      }}
    >
      <Handle type="target" position={Position.Left} aria-label="关系输入" />
      <div className="knowledge-graph-node-topline">
        {isDocument
          ? <FileText className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          : <Boxes className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />}
        <span>{isDocument ? '文档' : '实体'}</span>
      </div>
      <strong className="knowledge-graph-node-title">{data.label}</strong>
      {data.subtitle ? <span className="knowledge-graph-node-summary">{data.subtitle}</span> : null}
      {openable ? <span className="knowledge-graph-node-open">{openLabel} <ChevronRight className="h-3 w-3" aria-hidden="true" /></span> : null}
      <Handle type="source" position={Position.Right} aria-label="关系输出" />
    </div>
  );
}

const nodeTypes = { knowledge: KnowledgeNode };

export function KnowledgeGraph({
  graph,
  loading,
  error,
  onRetry,
  selectedDocumentId,
  onOpenDocument,
  viewportStorageKey,
}: KnowledgeGraphProps): JSX.Element {
  const projected = useMemo(
    () => buildKnowledgeGraph(graph, selectedDocumentId, onOpenDocument),
    [graph, onOpenDocument, selectedDocumentId],
  );
  const savedViewport = useMemo(() => readKnowledgeGraphViewport(viewportStorageKey), [viewportStorageKey]);
  const savedPositions = useMemo(() => readKnowledgeGraphPositions(viewportStorageKey), [viewportStorageKey]);
  const placedNodes = useMemo(
    () => projected.nodes.map((node) => (savedPositions[node.id] ? { ...node, position: savedPositions[node.id] } : node)),
    [projected.nodes, savedPositions],
  );
  const [nodes, setNodes] = useNodesState<KnowledgeGraphNode>(placedNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(projected.edges);

  const handleNodesChange = useCallback((changes: NodeChange<KnowledgeGraphNode>[]) => {
    setNodes((current) => {
      const next = applyNodeChanges(changes, current) as KnowledgeGraphNode[];
      writeKnowledgeGraphPositions(viewportStorageKey, next);
      return next;
    });
  }, [setNodes, viewportStorageKey]);

  const handleMoveEnd = useCallback((_event: unknown, viewport: Viewport) => {
    writeKnowledgeGraphViewport(viewportStorageKey, viewport);
  }, [viewportStorageKey]);

  useEffect(() => {
    setNodes((current) => {
      const next = reconcileKnowledgeGraphNodes(current, placedNodes);
      return isSameNodeList(current, next) ? current : next;
    });
  }, [placedNodes, setNodes]);

  useEffect(() => {
    setEdges((current) => (current === projected.edges ? current : projected.edges));
  }, [projected.edges, setEdges]);

  if (error) {
    return (
      <div className="knowledge-graph" data-testid="knowledge-graph" aria-label="知识全景画布">
        <div className="knowledge-graph-error" role="alert">
          <AlertCircle className="h-4 w-4 shrink-0 text-status-error" aria-hidden="true" />
          <span className="min-w-0 flex-1">{error}</span>
          <Button type="button" size="sm" onClick={onRetry}>
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
            重试
          </Button>
        </div>
      </div>
    );
  }

  if (!graph) {
    if (!loading) {
      return (
        <EmptyState
          className="knowledge-canvas-empty"
          icon={<AlertCircle className="h-5 w-5" aria-hidden="true" />}
          title="还没有读取知识快照"
          description="全景需要一个已发布的资料库快照，重试后才会建立。"
          action={<Button type="button" size="sm" onClick={onRetry}>重试</Button>}
        />
      );
    }
    return (
      <div className="knowledge-canvas-loading" role="status" aria-label="知识全景加载中">
        <LoaderCircle className="h-4 w-4 animate-spin text-brand-primary motion-reduce:animate-none" aria-hidden="true" />
        <span>正在读取知识全景…</span>
      </div>
    );
  }

  const { documents, entities } = collectKnowledgeGraphSources(graph);
  const truncated = documents.length + entities.length > MAX_KNOWLEDGE_GRAPH_NODES;
  const droppedRelations = graph.relations.length - projected.edges.length;

  if (projected.nodes.length === 0) {
    return (
      <EmptyState
        className="knowledge-canvas-empty"
        icon={<Boxes className="h-5 w-5" aria-hidden="true" />}
        title="这个发布没有可画的节点"
        description="快照里既没有文档也没有实体；只有已渲染的文档或实体才能作为全景节点，指向断言或未解析端点的关系不会补占位节点。"
        action={<Button type="button" size="sm" onClick={onRetry}>刷新快照</Button>}
      />
    );
  }

  return (
    <div className="knowledge-graph" data-testid="knowledge-graph" aria-label="知识全景画布">
      <div className="knowledge-graph-toolbar">
        <div className="min-w-0">
          <p className="text-body font-medium text-text-primary">知识全景</p>
          <p className="text-caption text-text-tertiary">
            {projected.nodes.length} 个节点（{documents.length} 文档 / {entities.length} 实体） · 已画 {projected.edges.length} 条关系
            {truncated ? ` · 超过 ${MAX_KNOWLEDGE_GRAPH_NODES} 个节点，只画前 ${MAX_KNOWLEDGE_GRAPH_NODES} 个` : ''}
          </p>
        </div>
        <div className="knowledge-graph-toolbar-actions">
          {loading ? (
            <span className="text-caption text-text-tertiary" role="status">
              <LoaderCircle className="mr-micro inline h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              正在刷新快照
            </span>
          ) : null}
          <Button type="button" size="sm" onClick={onRetry} disabled={loading}>
            <RefreshCw className={cx('h-3.5 w-3.5', loading && 'animate-spin motion-reduce:animate-none')} aria-hidden="true" />
            刷新
          </Button>
        </div>
      </div>
      {projected.edges.length === 0 ? (
        <p className="knowledge-canvas-partial-notice" role="status">这个发布没有能画出的关系：只有两端都是已渲染文档或实体的关系才会出现在全景里。</p>
      ) : droppedRelations > 0 ? (
        <p className="knowledge-canvas-partial-notice" role="status">{droppedRelations} 条关系指向断言或未渲染的端点，没有画到画布上。</p>
      ) : null}
      {truncated ? (
        <p className="knowledge-canvas-partial-notice" role="status">全景只画前 {MAX_KNOWLEDGE_GRAPH_NODES} 个节点（文档优先），拖动和缩放不会修改知识正文。</p>
      ) : null}
      <div className="knowledge-graph-flow">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={handleNodesChange}
          onEdgesChange={onEdgesChange}
          onMoveEnd={handleMoveEnd}
          defaultViewport={savedViewport ?? { x: 0, y: 0, zoom: 1 }}
          fitView={!savedViewport}
          fitViewOptions={{ padding: 0.2 }}
          minZoom={0.35}
          maxZoom={1.6}
          nodesDraggable
          nodesConnectable={false}
          panOnDrag
          zoomOnScroll
          aria-label="知识文档与实体关系图"
          ariaLabelConfig={GRAPH_ARIA_LABELS}
        >
          <Background gap={24} size={1} />
          <Controls showInteractive={false} position="bottom-left" aria-label="知识画布控件" />
          <MiniMap pannable zoomable ariaLabel="知识图谱缩略图" nodeColor="hsl(var(--color-brand-primary))" maskColor="hsl(var(--color-surface-base) / 0.72)" />
        </ReactFlow>
      </div>
    </div>
  );
}
