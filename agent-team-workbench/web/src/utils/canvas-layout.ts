export type CanvasNodeKind = 'start' | 'end' | 'process' | 'decision' | 'actor' | 'system' | 'note';

export interface CanvasLayoutNode {
  id: string;
  label: string;
  detail?: string;
  kind: CanvasNodeKind;
}

export interface CanvasLayoutEdge {
  from: string;
  to: string;
  label?: string;
}

export interface CanvasLayoutPosition {
  id: string;
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface CanvasLayoutResult {
  width: number;
  height: number;
  nodes: CanvasLayoutPosition[];
  edges: Array<CanvasLayoutEdge & { path: string }>;
}

const NODE_WIDTH = 168;
const NODE_HEIGHT = 56;
const DECISION_SIZE = 96;
const NOTE_WIDTH = 140;
const NOTE_HEIGHT = 48;
const GAP_X = 48;
const GAP_Y = 72;
const PADDING = 32;

function nodeSize(kind: CanvasNodeKind): { width: number; height: number } {
  if (kind === 'decision') return { width: DECISION_SIZE, height: DECISION_SIZE };
  if (kind === 'note') return { width: NOTE_WIDTH, height: NOTE_HEIGHT };
  return { width: NODE_WIDTH, height: NODE_HEIGHT };
}

function layerNodes(
  nodes: CanvasLayoutNode[],
  edges: CanvasLayoutEdge[],
): Map<string, number> {
  const incoming = new Map<string, number>();
  for (const node of nodes) incoming.set(node.id, 0);
  for (const edge of edges) {
    if (!incoming.has(edge.to)) continue;
    incoming.set(edge.to, (incoming.get(edge.to) ?? 0) + 1);
  }

  const layers = new Map<string, number>();
  const queue = nodes.filter((node) => (incoming.get(node.id) ?? 0) === 0).map((node) => node.id);
  if (!queue.length) queue.push(...nodes.map((node) => node.id));

  const seen = new Set<string>();
  while (queue.length) {
    const id = queue.shift();
    if (!id || seen.has(id)) continue;
    seen.add(id);
    const layer = layers.get(id) ?? 0;
    for (const edge of edges.filter((item) => item.from === id)) {
      const next = Math.max(layers.get(edge.to) ?? 0, layer + 1);
      layers.set(edge.to, next);
      if (!seen.has(edge.to)) queue.push(edge.to);
    }
    if (!layers.has(id)) layers.set(id, 0);
  }
  for (const node of nodes) {
    if (!layers.has(node.id)) layers.set(node.id, 0);
  }
  return layers;
}

function autoPositions(nodes: CanvasLayoutNode[], edges: CanvasLayoutEdge[]): CanvasLayoutPosition[] {
  const layers = layerNodes(nodes, edges);
  const grouped = new Map<number, CanvasLayoutNode[]>();
  for (const node of nodes) {
    const layer = layers.get(node.id) ?? 0;
    const bucket = grouped.get(layer) ?? [];
    bucket.push(node);
    grouped.set(layer, bucket);
  }

  const positions: CanvasLayoutPosition[] = [];
  const sortedLayers = [...grouped.keys()].sort((a, b) => a - b);
  let maxWidth = 0;

  for (const layer of sortedLayers) {
    const row = grouped.get(layer) ?? [];
    const rowWidth = row.reduce((sum, node, index) => {
      const size = nodeSize(node.kind);
      return sum + size.width + (index > 0 ? GAP_X : 0);
    }, 0);
    maxWidth = Math.max(maxWidth, rowWidth);
  }

  const canvasWidth = Math.max(maxWidth + PADDING * 2, 320);
  let currentY = PADDING;

  for (const layer of sortedLayers) {
    const row = grouped.get(layer) ?? [];
    const rowWidth = row.reduce((sum, node, index) => {
      const size = nodeSize(node.kind);
      return sum + size.width + (index > 0 ? GAP_X : 0);
    }, 0);
    const rowHeight = Math.max(...row.map((node) => nodeSize(node.kind).height));
    let currentX = (canvasWidth - rowWidth) / 2;
    for (const node of row) {
      const size = nodeSize(node.kind);
      positions.push({
        id: node.id,
        x: currentX,
        y: currentY + (rowHeight - size.height) / 2,
        width: size.width,
        height: size.height,
      });
      currentX += size.width + GAP_X;
    }
    currentY += rowHeight + GAP_Y;
  }

  return positions;
}

function anchorPoint(
  from: CanvasLayoutPosition,
  to: CanvasLayoutPosition,
): { start: { x: number; y: number }; end: { x: number; y: number } } {
  const fromCenter = { x: from.x + from.width / 2, y: from.y + from.height / 2 };
  const toCenter = { x: to.x + to.width / 2, y: to.y + to.height / 2 };
  const dx = toCenter.x - fromCenter.x;
  const dy = toCenter.y - fromCenter.y;

  if (Math.abs(dy) >= Math.abs(dx)) {
    if (dy >= 0) {
      return {
        start: { x: fromCenter.x, y: from.y + from.height },
        end: { x: toCenter.x, y: to.y },
      };
    }
    return {
      start: { x: fromCenter.x, y: from.y },
      end: { x: toCenter.x, y: to.y + to.height },
    };
  }

  if (dx >= 0) {
    return {
      start: { x: from.x + from.width, y: fromCenter.y },
      end: { x: to.x, y: toCenter.y },
    };
  }
  return {
    start: { x: from.x, y: fromCenter.y },
    end: { x: to.x + to.width, y: toCenter.y },
  };
}

function edgePath(start: { x: number; y: number }, end: { x: number; y: number }): string {
  const midY = (start.y + end.y) / 2;
  if (Math.abs(end.x - start.x) < 12) {
    return `M ${start.x} ${start.y} L ${end.x} ${end.y}`;
  }
  return `M ${start.x} ${start.y} C ${start.x} ${midY}, ${end.x} ${midY}, ${end.x} ${end.y}`;
}

export function layoutCanvas(
  nodes: CanvasLayoutNode[],
  edges: CanvasLayoutEdge[],
  explicit?: Array<{ id: string; x: number; y: number }>,
): CanvasLayoutResult {
  const positions = autoPositions(nodes, edges);
  const byId = new Map(positions.map((node) => [node.id, node]));

  if (explicit?.length) {
    for (const point of explicit) {
      const current = byId.get(point.id);
      const source = nodes.find((node) => node.id === point.id);
      if (!current || !source) continue;
      const size = nodeSize(source.kind);
      byId.set(point.id, {
        id: point.id,
        x: Math.max(0, Math.min(point.x, 1000 - size.width)),
        y: Math.max(0, Math.min(point.y, 1000 - size.height)),
        width: size.width,
        height: size.height,
      });
    }
  }

  const placed = [...byId.values()];
  const width = Math.max(...placed.map((node) => node.x + node.width), 320) + PADDING;
  const height = Math.max(...placed.map((node) => node.y + node.height), 120) + PADDING;

  const resolvedEdges = edges.flatMap((edge) => {
    const from = byId.get(edge.from);
    const to = byId.get(edge.to);
    if (!from || !to) return [];
    const { start, end } = anchorPoint(from, to);
    return [{ ...edge, path: edgePath(start, end) }];
  });

  return { width, height, nodes: placed, edges: resolvedEdges };
}
