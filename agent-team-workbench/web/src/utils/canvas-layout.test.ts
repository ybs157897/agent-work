import { describe, expect, it } from 'vitest';
import { layoutCanvas } from './canvas-layout';

describe('layoutCanvas', () => {
  it('lays out nodes in layers and draws edges between them', () => {
    const result = layoutCanvas(
      [
        { id: 'start', label: '开始', kind: 'start' },
        { id: 'review', label: '评审', kind: 'process' },
        { id: 'done', label: '完成', kind: 'end' },
      ],
      [
        { from: 'start', to: 'review', label: '提交' },
        { from: 'review', to: 'done' },
      ],
    );

    expect(result.nodes).toHaveLength(3);
    expect(result.edges).toHaveLength(2);
    expect(result.edges[0]?.path.startsWith('M ')).toBe(true);
    expect(result.nodes.find((node) => node.id === 'start')?.y ?? 0)
      .toBeLessThan(result.nodes.find((node) => node.id === 'done')?.y ?? 0);
  });

  it('honors explicit coordinates when provided', () => {
    const result = layoutCanvas(
      [{ id: 'a', label: 'A', kind: 'note' }, { id: 'b', label: 'B', kind: 'note' }],
      [{ from: 'a', to: 'b' }],
      [{ id: 'a', x: 40, y: 60 }, { id: 'b', x: 260, y: 180 }],
    );

    expect(result.nodes.find((node) => node.id === 'a')).toMatchObject({ x: 40, y: 60 });
    expect(result.nodes.find((node) => node.id === 'b')).toMatchObject({ x: 260, y: 180 });
  });
});
