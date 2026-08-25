import { describe, expect, it } from 'vitest';
import type { ModelEntry } from '../api/types';
import {
  buildEditModelPayload,
  generateProviderId,
  groupByProvider,
  isProviderDeleteConfirmed,
  isValidBaseUrl,
  modelDeleteHint,
  resolveModelRefId,
  slugifyModelRef,
} from './models/provider-utils';

const model = (
  id: string,
  provider: string,
  extra: Partial<ModelEntry> = {},
): ModelEntry => ({
  id,
  display_name: id,
  provider_id: extra.provider_id ?? `prov-${provider}`,
  provider,
  model: id,
  ...extra,
});

describe('groupByProvider', () => {
  it('groups models sharing provider_id under one supplier', () => {
    const groups = groupByProvider([
      model('deepseek-v4-flash', 'deepseek-official', {
        provider_id: 'prov-deepseek-official',
        category: 'DeepSeek',
        api_key_env: 'DEEPSEEK_API_KEY',
      }),
      model('deepseek-v4-pro', 'deepseek-official', {
        provider_id: 'prov-deepseek-official',
        category: 'DeepSeek',
        api_key_env: 'DEEPSEEK_API_KEY',
      }),
      model('ox-alpha', 'openrouter', {
        provider_id: 'prov-openrouter',
        category: 'OpenRouter',
        base_url: 'https://openrouter.ai/api/v1',
        api_key_env: 'OPENROUTER_API_KEY',
        api: 'openai-completions',
      }),
    ]);

    expect(groups).toHaveLength(2);
    const deepseek = groups.find((g) => g.id === 'prov-deepseek-official');
    expect(deepseek?.models).toHaveLength(2);
  });

  it('splits same provider route when provider_id differs', () => {
    const groups = groupByProvider([
      model('a', 'openrouter', { provider_id: 'prov-a', base_url: 'https://a.example/v1' }),
      model('b', 'openrouter', { provider_id: 'prov-b', base_url: 'https://b.example/v1' }),
    ]);
    expect(groups).toHaveLength(2);
  });
});

describe('isValidBaseUrl', () => {
  it('留空合法（走供应商默认端点）', () => {
    expect(isValidBaseUrl('')).toBe(true);
    expect(isValidBaseUrl('   ')).toBe(true);
  });

  it('http(s) 前缀合法，大小写不敏感', () => {
    expect(isValidBaseUrl('https://api.example.com/v1')).toBe(true);
    expect(isValidBaseUrl('http://localhost:8000')).toBe(true);
    expect(isValidBaseUrl('HTTPS://api.example.com')).toBe(true);
  });

  it('缺协议前缀或非 http 协议一律非法', () => {
    expect(isValidBaseUrl('api.example.com/v1')).toBe(false);
    expect(isValidBaseUrl('ftp://files.example.com')).toBe(false);
    expect(isValidBaseUrl('wss://rt.example.com')).toBe(false);
  });
});

describe('resolveModelRefId', () => {
  it('keeps valid ascii ref', () => {
    expect(resolveModelRefId('kimi-k2-7', 'glm-4', '')).toBe('kimi-k2-7');
  });

  it('ignores ref with invalid characters', () => {
    expect(resolveModelRefId('kimi-k2.7-code', 'glm-4-flash', '')).toBe('glm-4-flash');
  });

  it('returns undefined when only chinese names are provided', () => {
    expect(resolveModelRefId('中文模型', '显示名', '通义千问')).toBeUndefined();
  });

  it('slugifies mixed names', () => {
    expect(slugifyModelRef('GLM-4 Flash')).toBe('glm-4-flash');
  });
});

describe('buildEditModelPayload（EditModelModal PUT payload）', () => {
  it('provider / provider_id 原样透传；category 仅作 UI 字段，不得写进 provider 路由', () => {
    const entry = model('glm-4-flash', 'zhipu-gateway', {
      provider_id: 'prov-zhipu',
      category: '智谱 GLM',
      api: 'openai-completions',
      base_url: 'https://open.zhipu.ai/api/paas/v4',
      context_window: 128000,
      max_tokens: 4096,
      notes: '旧备注',
    });
    const payload = buildEditModelPayload(entry, {
      displayName: 'GLM 4 Flash',
      model: 'glm-4-flash',
      contextWindow: 256000,
      maxTokens: undefined,
      notes: '',
    });

    expect(payload.provider).toBe('zhipu-gateway'); // 修复点：不再被 entry.category 污染
    expect(payload.provider_id).toBe('prov-zhipu'); // 修复点：全量 PUT 不再丢失
    expect(payload.category).toBe('智谱 GLM'); // UI 分类保留但仅作展示
    expect(payload.model).toBe('glm-4-flash');
    expect(payload.display_name).toBe('GLM 4 Flash');
    expect(payload.context_window).toBe(256000);
    expect(payload.max_tokens).toBeUndefined();
    expect(payload.notes).toBe('');
  });
});

describe('generateProviderId', () => {
  it('uses provider slug when ascii', () => {
    expect(generateProviderId('DeepSeek', 'deepseek-official')).toBe('prov-deepseek-official');
  });

  it('uses random id for chinese-only names', () => {
    const id = generateProviderId('智谱', '智谱');
    expect(id.startsWith('prov-')).toBe(true);
    expect(id).not.toBe('prov-model');
  });
});

describe('delete confirmation helpers', () => {
  it('requires exact provider label to confirm provider delete', () => {
    expect(isProviderDeleteConfirmed('DeepSeek', 'DeepSeek')).toBe(true);
    expect(isProviderDeleteConfirmed('DeepSeek', 'deepseek')).toBe(false);
    expect(isProviderDeleteConfirmed('DeepSeek', ' DeepSeek ')).toBe(true);
  });

  it('warns when deleting the last model under a provider', () => {
    expect(modelDeleteHint(1)).toMatch(/最后一个模型/);
    expect(modelDeleteHint(2)).toMatch(/Agent/);
  });
});
