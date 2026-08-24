import { describe, expect, it } from 'vitest';
import type { ChatMessage } from '../stores/chat.store';
import { aggregateTurnDiff } from './turn-diff';

const tool = (over: Partial<ChatMessage>): ChatMessage => ({
  key: 'k',
  runId: 'r1',
  kind: 'tool',
  text: '',
  at: '',
  toolStatus: 'success',
  ...over,
});

const SAMPLE_DIFF = `--- a/foo.ts
+++ b/foo.ts
@@ -1 +1 @@
-old
+new
`;

describe('aggregateTurnDiff', () => {
  it('忽略非 write/edit 与无 diff 输出', () => {
    expect(
      aggregateTurnDiff([
        tool({ tool: 'bash', detail: 'stdout' }),
        tool({ tool: 'read', detail: SAMPLE_DIFF }),
      ]),
    ).toBeNull();
  });

  it('合并 write/edit 的 unified diff', () => {
    const out = aggregateTurnDiff([
      tool({ tool: 'apply_patch', detail: SAMPLE_DIFF }),
      tool({ tool: 'edit', detail: SAMPLE_DIFF }),
    ]);
    expect(out).toContain('--- a/foo.ts');
    expect(out!.split('--- a/foo.ts').length).toBeGreaterThan(2);
  });
});
