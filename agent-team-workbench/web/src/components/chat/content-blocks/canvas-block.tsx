import { useId, useMemo } from 'react';
import { Workflow } from 'lucide-react';
import type { CanvasBlock as CanvasBlockValue, CanvasNodeKind } from '../../../utils/content-blocks';
import { layoutCanvas } from '../../../utils/canvas-layout';
import { ContentBlockShell } from './content-block-shell';

const KIND_LABELS: Record<CanvasNodeKind, string> = {
  start: '起点',
  end: '终点',
  process: '步骤',
  decision: '决策',
  actor: '角色',
  system: '系统',
  note: '备注',
};

export function CanvasBlock({ block }: { block: CanvasBlockValue }) {
  const markerId = useId().replace(/:/g, '');
  const layout = useMemo(
    () => layoutCanvas(
      block.nodes,
      block.edges,
      block.nodes.flatMap((node) => (
        node.x !== undefined && node.y !== undefined
          ? [{ id: node.id, x: node.x, y: node.y }]
          : []
      )),
    ),
    [block.nodes, block.edges],
  );
  const nodeById = useMemo(() => new Map(block.nodes.map((node) => [node.id, node])), [block.nodes]);
  const summary = `${block.title ?? '流程画布'}，${block.nodes.length} 个节点，${block.edges.length} 条连线`;

  return (
    <ContentBlockShell block={block} icon={Workflow}>
      <div className="chat-content-canvas" role="img" aria-label={summary}>
        <svg
          className="chat-content-canvas-svg"
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          preserveAspectRatio="xMidYMid meet"
          aria-hidden
        >
          <defs>
            <marker id={markerId} markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
              <path d="M0,0 L8,4 L0,8 Z" className="chat-content-canvas-arrow-head" />
            </marker>
          </defs>
          {layout.edges.map((edge, index) => (
            <g key={`${edge.from}-${edge.to}-${index}`} className="chat-content-canvas-edge">
              <path d={edge.path} className="chat-content-canvas-edge-line" markerEnd={`url(#${markerId})`} />
              {edge.label && (
                <text className="chat-content-canvas-edge-label">
                  <textPath href={`#chat-canvas-edge-${index}`} startOffset="50%" textAnchor="middle">
                    {edge.label}
                  </textPath>
                </text>
              )}
              <path id={`chat-canvas-edge-${index}`} d={edge.path} className="chat-content-canvas-edge-guide" />
            </g>
          ))}
        </svg>
        <div
          className="chat-content-canvas-stage"
          style={{ aspectRatio: `${layout.width} / ${layout.height}` }}
        >
          {layout.nodes.map((position) => {
            const node = nodeById.get(position.id);
            if (!node) return null;
            const left = (position.x / layout.width) * 100;
            const top = (position.y / layout.height) * 100;
            const width = (position.width / layout.width) * 100;
            const height = (position.height / layout.height) * 100;
            return (
              <article
                key={node.id}
                className={`chat-content-canvas-node chat-content-canvas-node-${node.kind}`}
                style={{ left: `${left}%`, top: `${top}%`, width: `${width}%`, height: `${height}%` }}
                aria-label={`${KIND_LABELS[node.kind]}：${node.label}`}
              >
                <span className="chat-content-canvas-node-kind">{KIND_LABELS[node.kind]}</span>
                <strong className="chat-content-canvas-node-label">{node.label}</strong>
                {node.detail && <p className="chat-content-canvas-node-detail">{node.detail}</p>}
              </article>
            );
          })}
        </div>
      </div>
      <details className="chat-content-canvas-data">
        <summary>查看节点清单</summary>
        <ul className="chat-content-canvas-list">
          {block.nodes.map((node) => (
            <li key={node.id}>
              <span className="chat-content-canvas-list-kind">{KIND_LABELS[node.kind]}</span>
              <span className="chat-content-canvas-list-label">{node.label}</span>
              {node.detail && <span className="chat-content-canvas-list-detail">{node.detail}</span>}
            </li>
          ))}
        </ul>
      </details>
    </ContentBlockShell>
  );
}
