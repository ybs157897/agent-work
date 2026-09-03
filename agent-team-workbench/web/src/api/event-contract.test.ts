import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { EVENT_NAMES, INTERNAL_EVENT_NAMES } from './types';

/**
 * 浏览器 EventSource 按 EVENT_NAMES 显式注册 listener；若少一个 AsyncAPI 枚举，
 * 服务端仍会发布但 Web 永远收不到。这里直接读仓库 canonical AsyncAPI，双向对账。
 *
 * internal 事件（Run Journal：run.phase_*、run.log_chunk、run.decision）只落
 * run_events、不经 SSE 推送，故对账形状是 EVENT_NAMES ∪ INTERNAL_EVENT_NAMES
 * == AsyncAPI enum，且两个集合不相交（internal 误进 SSE 名单会静默注册
 * 永远收不到的 listener；surface 事件漏进 internal 名单则前端永远收不到）。
 */
function asyncAPIEventNames(): string[] {
  const path = fileURLToPath(new URL('../../../contracts/events/asyncapi.yaml', import.meta.url));
  const source = readFileSync(path, 'utf8');
  const schemaAt = source.indexOf('    CanonicalEventEnvelope:');
  const enumAt = source.indexOf('          enum:', schemaAt);
  const endAt = source.indexOf('        occurred_at:', enumAt);
  if (schemaAt < 0 || enumAt < 0 || endAt < 0) throw new Error('AsyncAPI CanonicalEventEnvelope.type.enum 未找到');
  return [...source.slice(enumAt, endAt).matchAll(/^\s*-\s+([\w.]+)\s*$/gm)].map((match) => match[1]);
}

describe('Web EVENT_NAMES ↔ AsyncAPI', () => {
  it('SSE 名单 ∪ internal 名单与 AsyncAPI 双向一致，且两名单不相交', () => {
    const sseNames: Set<string> = new Set(EVENT_NAMES);
    const internalNames: Set<string> = new Set(INTERNAL_EVENT_NAMES);
    for (const name of sseNames) {
      expect(internalNames.has(name), `${name} 同时出现在 SSE 与 internal 名单`).toBe(false);
    }
    expect(new Set([...sseNames, ...internalNames])).toEqual(new Set(asyncAPIEventNames()));
  });
});
