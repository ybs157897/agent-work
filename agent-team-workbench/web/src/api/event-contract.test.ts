import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { EVENT_NAMES } from './types';

/**
 * 浏览器 EventSource 按 EVENT_NAMES 显式注册 listener；若少一个 AsyncAPI 枚举，
 * 服务端仍会发布但 Web 永远收不到。这里直接读仓库 canonical AsyncAPI，双向对账。
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
  it('双向一致，避免 EventSource listener 与服务端事件目录漂移', () => {
    expect(new Set(EVENT_NAMES)).toEqual(new Set(asyncAPIEventNames()));
  });
});
