import type { UpsertModelInput } from '../../api/endpoints';
import type { ModelEntry } from '../../api/types';

const MODEL_ID_RE = /^[a-z0-9][a-z0-9-]*$/;

/**
 * EditModelModal 的 PUT payload：provider / provider_id 必须原样透传，
 * category 只是 UI 展示分类（不参与 pi-ai 路由），不得写进 provider 字段；
 * PUT 是全量替换，漏传 provider_id 会导致保存后凭据匹配与供应商分组失效。
 */
export function buildEditModelPayload(
  entry: ModelEntry,
  draft: {
    displayName: string;
    model: string;
    contextWindow?: number;
    maxTokens?: number;
    notes: string;
  },
): UpsertModelInput {
  return {
    display_name: draft.displayName,
    category: entry.category,
    provider_id: entry.provider_id,
    provider: entry.provider,
    api: entry.api,
    model: draft.model,
    base_url: entry.base_url,
    context_window: draft.contextWindow,
    max_tokens: draft.maxTokens,
    notes: draft.notes,
  };
}

/** 将用户输入转为合法注册表 ref；非法字符时从模型名推导，仍无法推导则留空由后端生成。 */
export function slugifyModelRef(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

export function resolveModelRefId(
  ref: string,
  modelName: string,
  displayName = '',
): string | undefined {
  const raw = ref.trim();
  if (raw && MODEL_ID_RE.test(raw)) return raw;
  const fromModel = slugifyModelRef(modelName);
  if (fromModel) return fromModel;
  const fromDisplay = slugifyModelRef(displayName);
  return fromDisplay || undefined;
}

/** 新建供应商时生成稳定 provider id（与后端 GenerateProviderID 对齐）。 */
export function generateProviderId(label: string, provider: string): string {
  const slug = slugifyModelRef(provider) || slugifyModelRef(label);
  if (slug) return `prov-${slug}`;
  return `prov-${crypto.randomUUID().replace(/-/g, '').slice(0, 12)}`;
}

export interface ModelProviderGroup {
  id: string;
  label: string;
  provider: string;
  base_url: string;
  api: ModelEntry['api'];
  api_key_env?: string;
  models: ModelEntry[];
}

const PROVIDER_LABELS: Record<string, string> = {
  'deepseek-official': 'DeepSeek',
  deepseek: 'DeepSeek',
  openrouter: 'OpenRouter',
  moonshot: 'Kimi',
  kimi: 'Kimi',
  dashscope: 'Qwen',
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  zhipu: '智谱',
};

export function providerLabel(models: ModelEntry[]): string {
  const first = models[0];
  if (!first) return '未命名供应商';
  return first.category?.trim() || PROVIDER_LABELS[first.provider.toLowerCase()] || first.provider;
}

/** 按供应商 id 分组；组内模型按显示名排序。 */
export function groupByProvider(models: ModelEntry[]): ModelProviderGroup[] {
  const map = new Map<string, ModelEntry[]>();
  for (const m of models) {
    if (!m.provider_id) continue;
    map.set(m.provider_id, [...(map.get(m.provider_id) ?? []), m]);
  }
  return [...map.entries()]
    .map(([id, entries]) => {
      const sorted = [...entries].sort((a, b) =>
        (a.display_name || a.id).localeCompare(b.display_name || b.id, 'zh-CN'),
      );
      const head = sorted[0]!;
      return {
        id,
        label: providerLabel(sorted),
        provider: head.provider,
        base_url: head.base_url ?? '',
        api: head.api,
        api_key_env: head.api_key_env,
        models: sorted,
      };
    })
    .sort((a, b) => a.label.localeCompare(b.label, 'zh-CN'));
}

export function formatContextBadge(tokens?: number): string | null {
  if (!tokens || tokens <= 0) return null;
  if (tokens >= 1_000_000) return `${Math.round(tokens / 1_000_000)}M`;
  if (tokens >= 1_000) return `${Math.round(tokens / 1_000)}K`;
  return String(tokens);
}

/** Base URL 留空合法（走供应商默认端点）；填写则必须是 http(s) URL。 */
export function isValidBaseUrl(value: string): boolean {
  const url = value.trim();
  return url === '' || /^https?:\/\//i.test(url);
}

/** 删除供应商时需输入的确认文案（与 label 精确匹配）。 */
export function providerDeleteConfirmLabel(label: string): string {
  return label.trim();
}

export function isProviderDeleteConfirmed(label: string, typed: string): boolean {
  return typed.trim() === providerDeleteConfirmLabel(label);
}

/** 删除单个模型时的补充说明（最后一个模型会连带移除供应商分组）。 */
export function modelDeleteHint(modelCountInProvider: number): string | null {
  if (modelCountInProvider <= 1) {
    return '这是该供应商下的最后一个模型。删除后供应商分组与本机 API Key 也会一并移除。';
  }
  return '已绑定此 ref 的 Agent 在下次运行前需改选其他模型。';
}

