import { Cpu, Eye, EyeOff, Pencil, Plus, RefreshCw, Settings2, Trash2, X } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { ApiError } from '../api/client';
import { createModel, deleteModel, getProviderCredential, listModels, putProviderCredential, updateModel } from '../api/endpoints';
import type { ModelEntry } from '../api/types';
import { Drawer } from '../components/drawer';
import { Modal } from '../components/modal';
import { Button, FieldError, Select, Skeleton } from '../components/ui';
import {
  ConfigAvatar,
  ConfigEmptyState,
  ConfigFooter,
  ConfigFormCard,
  ConfigMain,
  ConfigPage,
  ConfigPageHeader,
  ConfigPanel,
  ConfigSection,
  ConfigSidebar,
  ConfigSidebarItem,
  ConfigSplit,
  ConfigToolbar,
  configInputCls,
  configInputInvalidCls,
  configLabelCls,
} from '../components/config-workbench';
import { toast } from '../stores/toast.store';
import {
  buildEditModelPayload,
  formatContextBadge,
  generateProviderId,
  groupByProvider,
  isProviderDeleteConfirmed,
  isValidBaseUrl,
  modelDeleteHint,
  resolveModelRefId,
  type ModelProviderGroup,
} from './models/provider-utils';

const ADD_PROVIDER_KEY = '__add_provider__';

function modelApiError(err: unknown, fallback: string): string {
  if (!(err instanceof ApiError)) return fallback;
  if (err.code === 'model_exists') return err.message;
  const detail = err.message.replace(/^domain validation failed:\s*/i, '');
  if (detail.includes('id 必须是小写 slug')) {
    return '模型 ref 只能用小写英文、数字和连字符；留空将根据 API 模型名自动生成';
  }
  if (detail.includes('base_url 必须是 http(s) URL')) {
    return 'Base URL 必须以 http:// 或 https:// 开头';
  }
  if (detail.includes('配置了 base_url 时必须指定 api')) {
    return '填写 Base URL 后需选择 API 格式（默认选 Chat Completions 即可）';
  }
  return detail || fallback;
}

const API_OPTIONS = [
  { value: '', label: 'Chat Completions（默认）' },
  { value: 'openai-completions', label: 'Chat Completions (/chat/completions)' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
  { value: 'anthropic-messages', label: 'Anthropic Messages (/v1/messages)' },
] as const;

const inputCls = configInputCls;
const inputInvalidCls = configInputInvalidCls;

type DraftModel = {
  key: string;
  id: string;
  model: string;
  displayName: string;
};

function newDraftModel(): DraftModel {
  return { key: crypto.randomUUID(), id: '', model: '', displayName: '' };
}

/** 模型设置：左侧供应商列表 + 右侧配置面板（参考常见网关 UI）。 */
export default function ModelsPage() {
  const [models, setModels] = useState<ModelEntry[] | null>(null);
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [addingModel, setAddingModel] = useState(false);
  const [editingModel, setEditingModel] = useState<ModelEntry | null>(null);
  const [deletingModel, setDeletingModel] = useState<ModelEntry | null>(null);
  const [deletingProvider, setDeletingProvider] = useState<ModelProviderGroup | null>(null);

  const providers = useMemo(() => (models ? groupByProvider(models) : []), [models]);
  const isAddingProvider = selectedKey === ADD_PROVIDER_KEY;
  const selected = isAddingProvider ? null : (providers.find((p) => p.id === selectedKey) ?? providers[0] ?? null);

  const load = () => {
    setRefreshing(true);
    listModels()
      .then(({ items }) => {
        setModels(items);
        setSelectedKey((prev) => {
          if (prev === ADD_PROVIDER_KEY) return prev;
          if (prev && items.some((m) => m.provider_id === prev)) return prev;
          return items.length ? items[0]!.provider_id! : null;
        });
      })
      .catch((err) => toast.error(err instanceof ApiError ? err.message : '加载模型注册表失败'))
      .finally(() => setRefreshing(false));
  };

  useEffect(load, []);

  const afterRegistryChange = (removedProviderId?: string) => {
    setDeletingModel(null);
    setDeletingProvider(null);
    setRefreshing(true);
    listModels()
      .then(({ items }) => {
        setModels(items);
        setSelectedKey((prev) => {
          if (removedProviderId && prev === removedProviderId) {
            return items[0]?.provider_id ?? null;
          }
          if (prev === ADD_PROVIDER_KEY) return prev;
          if (prev && items.some((m) => m.provider_id === prev)) return prev;
          return items.length ? items[0]!.provider_id! : null;
        });
      })
      .catch((err) => toast.error(err instanceof ApiError ? err.message : '加载模型注册表失败'))
      .finally(() => setRefreshing(false));
  };

  const deleteModelEntry = async (m: ModelEntry, modelCountInProvider: number) => {
    await deleteModel(m.id);
    if (modelCountInProvider <= 1 && m.provider_id) {
      await putProviderCredential(m.provider_id, '');
    }
    toast.success(`已删除模型 ${m.id}`);
    afterRegistryChange(modelCountInProvider <= 1 ? m.provider_id : undefined);
  };

  const deleteProviderGroup = async (group: ModelProviderGroup) => {
    await Promise.all(group.models.map((m) => deleteModel(m.id)));
    await putProviderCredential(group.id, '');
    toast.success(`已删除供应商「${group.label}」`);
    afterRegistryChange(group.id);
  };

  const startAddProvider = () => setSelectedKey(ADD_PROVIDER_KEY);

  return (
    <ConfigPage>
      <ConfigPageHeader
        title="模型设置"
        subtitle="管理自定义模型供应商；配置完成后可在对话与 Agent 中按 ref 选用"
        actions={
          <Button onClick={load} disabled={refreshing}>
            <RefreshCw className={`w-4 h-4 ${refreshing ? 'animate-spin' : ''}`} />
            刷新
          </Button>
        }
      />

      <ConfigSplit>
        <ConfigSidebar
          title="供应商"
          footer={
            <button
              type="button"
              onClick={startAddProvider}
              className={`w-full flex items-center justify-center gap-1.5 border border-dashed rounded-lg py-2.5 text-body transition-colors ${
                isAddingProvider
                  ? 'border-brand-primary bg-brand-primary/10 text-brand-primary'
                  : 'border-border-strong text-text-secondary hover:bg-surface-base hover:text-text-primary'
              }`}
            >
              <Plus className="w-4 h-4" />
              添加供应商
            </button>
          }
        >
          {models === null ? (
            <div className="space-y-1 p-2">
              <Skeleton className="h-12 w-full" />
              <Skeleton className="h-12 w-full" />
              <Skeleton className="h-12 w-3/4" />
            </div>
          ) : providers.length === 0 && !isAddingProvider ? (
            <p className="text-caption text-text-tertiary px-2 py-3">暂无供应商，点击下方添加</p>
          ) : (
            providers.map((p) => (
              <ConfigSidebarItem
                key={p.id}
                active={selected?.id === p.id && !isAddingProvider}
                onClick={() => setSelectedKey(p.id)}
                leading={<ConfigAvatar label={p.label} />}
                title={p.label}
                subtitle={p.base_url || '未配置 Base URL'}
              />
            ))
          )}
        </ConfigSidebar>

        <ConfigMain>
          {isAddingProvider ? (
            <AddProviderPanel
              onCancel={() => setSelectedKey(providers[0]?.id ?? null)}
              onSaved={(key) => {
                setSelectedKey(key);
                load();
              }}
            />
          ) : !selected ? (
            <ConfigEmptyState icon={Cpu} title="选择左侧供应商进行配置" hint="或点击「添加供应商」创建新连接" />
          ) : (
            <ProviderPanel
              group={selected}
              onRefresh={load}
              onAddModel={() => setAddingModel(true)}
              onEditModel={setEditingModel}
              onDeleteModel={setDeletingModel}
              onDeleteProvider={() => setDeletingProvider(selected)}
            />
          )}
        </ConfigMain>
      </ConfigSplit>

      {addingModel && selected && (
        <AddModelModal
          group={selected}
          onClose={() => setAddingModel(false)}
          onSaved={() => {
            setAddingModel(false);
            load();
          }}
        />
      )}
      {editingModel && (
        <EditModelModal
          entry={editingModel}
          onClose={() => setEditingModel(null)}
          onSaved={() => {
            setEditingModel(null);
            load();
          }}
        />
      )}
      {deletingModel && selected && (
        <DeleteModelModal
          entry={deletingModel}
          modelCountInProvider={selected.models.length}
          onClose={() => setDeletingModel(null)}
          onConfirm={() => deleteModelEntry(deletingModel, selected.models.length)}
        />
      )}
      {deletingProvider && (
        <DeleteProviderModal
          group={deletingProvider}
          onClose={() => setDeletingProvider(null)}
          onConfirm={() => deleteProviderGroup(deletingProvider)}
        />
      )}
    </ConfigPage>
  );
}

/** 右侧内嵌：添加供应商（含模型列表，一次提交）。 */
function AddProviderPanel({
  onCancel,
  onSaved,
}: {
  onCancel: () => void;
  onSaved: (providerId: string) => void;
}) {
  const [label, setLabel] = useState('');
  const [baseURL, setBaseURL] = useState('');
  const [api, setApi] = useState<ModelEntry['api']>('openai-completions');
  const [apiKey, setApiKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [draftModels, setDraftModels] = useState<DraftModel[]>([newDraftModel()]);
  const [saving, setSaving] = useState(false);

  const updateDraft = (key: string, patch: Partial<DraftModel>) => {
    setDraftModels((rows) => rows.map((r) => (r.key === key ? { ...r, ...patch } : r)));
  };

  const removeDraft = (key: string) => {
    setDraftModels((rows) => (rows.length <= 1 ? rows : rows.filter((r) => r.key !== key)));
  };

  const validModels = draftModels.filter((m) => m.model.trim());
  const baseURLError = isValidBaseUrl(baseURL) ? '' : 'Base URL 需以 http:// 或 https:// 开头';
  const canSubmit = label.trim() && validModels.length > 0 && !baseURLError;

  const submit = async () => {
    if (!canSubmit) return;
    const name = label.trim();
    const providerId = generateProviderId(name, name);
    setSaving(true);
    try {
      for (const m of validModels) {
        await createModel({
          id: resolveModelRefId(m.id, m.model, m.displayName),
          display_name: m.displayName.trim() || m.id.trim() || m.model.trim(),
          category: name,
          provider_id: providerId,
          provider: name,
          api,
          model: m.model.trim(),
          base_url: baseURL.trim(),
        });
      }
      if (apiKey.trim()) {
        await putProviderCredential(providerId, apiKey.trim());
      }
      toast.success(`已添加供应商「${label.trim()}」`);
      onSaved(providerId);
    } catch (err) {
      toast.error(modelApiError(err, '创建失败'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <ConfigPanel>
      <div className="mb-4">
        <h3 className="text-h3 text-text-primary">添加模型供应商</h3>
        <p className="text-caption text-text-tertiary mt-1">配置完全自定义的 API 端点，并添加一个或多个模型。</p>
      </div>

      <ConfigFormCard>
        <ConfigSection title="连接配置">
          <label className="block">
            <span className={configLabelCls}>名称</span>
            <input
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              placeholder="如：智谱 GLM"
              className={inputCls}
            />
          </label>
          <label className="block">
            <span className={configLabelCls}>Base URL</span>
            <input
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder="https://api.example.com/v1"
              aria-invalid={!!baseURLError || undefined}
              className={`${baseURLError ? inputInvalidCls : inputCls} font-mono`}
            />
            {baseURLError ? <FieldError>{baseURLError}</FieldError> : null}
          </label>
          <label className="block">
            <span className={configLabelCls}>API Key</span>
            <div className="relative">
              <input
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                type={showKey ? 'text' : 'password'}
                placeholder="输入 API Key"
                className={`${inputCls} pr-10 font-mono`}
              />
              <button
                type="button"
                onClick={() => setShowKey((v) => !v)}
                className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-text-tertiary hover:text-text-primary"
              >
                {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </label>
          <label className="block">
            <span className={configLabelCls}>API 格式</span>
            <Select value={api} onChange={(e) => setApi(e.target.value as ModelEntry['api'])}>
              {API_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          </label>
        </ConfigSection>

        <ConfigSection title="模型列表">
          <div className="space-y-2">
            {draftModels.map((m) => (
              <div
                key={m.key}
                className="rounded-lg border border-border-subtle bg-surface-base p-3 space-y-2"
              >
                <div className="flex items-start gap-2">
                  <div className="flex-1 grid grid-cols-1 sm:grid-cols-2 gap-2">
                    <label className="block">
                      <span className={configLabelCls}>注册表 ref（可留空）</span>
                      <input
                        value={m.id}
                        onChange={(e) => updateDraft(m.key, { id: e.target.value })}
                        placeholder="留空则根据 API 模型名自动生成"
                        className={`${inputCls} font-mono text-caption`}
                      />
                    </label>
                    <label className="block">
                      <span className={configLabelCls}>API 模型名</span>
                      <input
                        value={m.model}
                        onChange={(e) => updateDraft(m.key, { model: e.target.value })}
                        placeholder="kimi-k2.7-code"
                        className={`${inputCls} font-mono text-caption`}
                      />
                    </label>
                  </div>
                  <button
                    type="button"
                    onClick={() => removeDraft(m.key)}
                    className="mt-6 p-1.5 rounded-md text-text-tertiary hover:text-status-error hover:bg-status-error/10"
                    title="移除"
                  >
                    <X className="w-4 h-4" />
                  </button>
                </div>
                <label className="block">
                  <span className={configLabelCls}>显示名（可选）</span>
                  <input
                    value={m.displayName}
                    onChange={(e) => updateDraft(m.key, { displayName: e.target.value })}
                    placeholder="Kimi K2.7 Code"
                    className={`${inputCls} text-caption`}
                  />
                </label>
              </div>
            ))}
          </div>
          <button
            type="button"
            onClick={() => setDraftModels((rows) => [...rows, newDraftModel()])}
            className="w-full flex items-center justify-center gap-1.5 border border-dashed border-border-strong rounded-lg py-2.5 text-body text-text-secondary hover:bg-surface-raised hover:text-text-primary transition-colors"
          >
            <Plus className="w-4 h-4" />
            添加模型
          </button>
        </ConfigSection>
      </ConfigFormCard>

      <ConfigFooter
        onCancel={onCancel}
        onSave={() => void submit()}
        saving={saving}
        saveDisabled={!canSubmit}
        saveLabel="添加供应商"
      />
    </ConfigPanel>
  );
}

function ProviderPanel({
  group,
  onRefresh,
  onAddModel,
  onEditModel,
  onDeleteModel,
  onDeleteProvider,
}: {
  group: ModelProviderGroup;
  onRefresh: () => void;
  onAddModel: () => void;
  onEditModel: (m: ModelEntry) => void;
  onDeleteModel: (m: ModelEntry) => void;
  onDeleteProvider: () => void;
}) {
  const [label, setLabel] = useState(group.label);
  const [baseURL, setBaseURL] = useState(group.base_url);
  const [api, setApi] = useState<ModelEntry['api']>(group.api ?? '');
  const [apiKey, setApiKey] = useState('');
  const [savedApiKey, setSavedApiKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [saving, setSaving] = useState(false);
  const [loadingKey, setLoadingKey] = useState(true);

  useEffect(() => {
    setLabel(group.label);
    setBaseURL(group.base_url);
    setApi(group.api ?? '');
  }, [group.id, group.label, group.base_url, group.api]);

  useEffect(() => {
    let cancelled = false;
    setLoadingKey(true);
    getProviderCredential(group.id)
      .then(({ api_key }) => {
        if (cancelled) return;
        setApiKey(api_key);
        setSavedApiKey(api_key);
      })
      .catch((err) => toast.error(err instanceof ApiError ? err.message : '加载凭据失败'))
      .finally(() => {
        if (!cancelled) setLoadingKey(false);
      });
    return () => {
      cancelled = true;
    };
  }, [group.id]);

  const saveProvider = async () => {
    setSaving(true);
    try {
      const name = label.trim();
      const resolvedAPI = api || (baseURL.trim() ? 'openai-completions' : '');
      await putProviderCredential(group.id, apiKey.trim());
      for (const m of group.models) {
        await updateModel(m.id, {
          display_name: m.display_name,
          category: name,
          provider_id: group.id,
          provider: group.provider,
          api: resolvedAPI as ModelEntry['api'],
          model: m.model,
          base_url: baseURL.trim(),
          context_window: m.context_window,
          max_tokens: m.max_tokens,
          notes: m.notes,
        });
      }
      setSavedApiKey(apiKey.trim());
      toast.success('供应商配置已保存');
      onRefresh();
    } catch (err) {
      toast.error(modelApiError(err, '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  const dirty =
    label !== group.label ||
    baseURL !== group.base_url ||
    (api ?? '') !== (group.api ?? '') ||
    apiKey !== savedApiKey;
  const baseURLError = isValidBaseUrl(baseURL) ? '' : 'Base URL 需以 http:// 或 https:// 开头';

  return (
    <ConfigPanel>
      <ConfigFormCard>
        <ConfigToolbar>
          <div className="flex items-center gap-2 min-w-0">
            <h3 className="text-h3 text-text-primary truncate">{group.label}</h3>
            <span className="shrink-0 rounded-full bg-status-success/15 text-status-success text-caption px-2.5 py-0.5 font-medium">
              已启用
            </span>
          </div>
        </ConfigToolbar>

        <ConfigSection title="连接配置">
          <label className="block">
            <span className={configLabelCls}>名称</span>
            <input value={label} onChange={(e) => setLabel(e.target.value)} className={inputCls} />
          </label>
          <label className="block">
            <span className={configLabelCls}>Base URL</span>
            <input
              value={baseURL}
              onChange={(e) => setBaseURL(e.target.value)}
              placeholder="https://api.example.com/v1"
              aria-invalid={!!baseURLError || undefined}
              className={`${baseURLError ? inputInvalidCls : inputCls} font-mono`}
            />
            {baseURLError ? <FieldError>{baseURLError}</FieldError> : null}
          </label>
          <label className="block">
            <span className={configLabelCls}>API Key</span>
            <div className="relative">
              <input
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                type={showKey ? 'text' : 'password'}
                placeholder={loadingKey ? '加载中…' : '输入 API Key'}
                disabled={loadingKey}
                className={`${inputCls} pr-10 font-mono`}
              />
              <button
                type="button"
                onClick={() => setShowKey((v) => !v)}
                className="absolute right-2 top-1/2 -translate-y-1/2 p-1 text-text-tertiary hover:text-text-primary"
              >
                {showKey ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
              </button>
            </div>
          </label>
          <label className="block">
            <span className={configLabelCls}>API 格式</span>
            <Select value={api} onChange={(e) => setApi(e.target.value as ModelEntry['api'])}>
              {API_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          </label>
        </ConfigSection>

        <ConfigSection title="模型列表" hint={`共 ${group.models.length} 个模型`}>
          <div className="space-y-2">
            {group.models.map((m) => {
              const badge = formatContextBadge(m.context_window);
              return (
                <div
                  key={m.id}
                  className="flex items-center gap-3 rounded-lg border border-border-subtle bg-surface-base px-3 py-2.5 transition-colors hover:border-border-strong hover:bg-surface-raised/60"
                >
                  <div className="min-w-0 flex-1">
                    <div className="text-body font-medium text-text-primary truncate">{m.display_name || m.id}</div>
                    <div className="text-caption text-text-tertiary font-mono truncate">{m.model}</div>
                  </div>
                  {badge ? (
                    <span className="shrink-0 rounded-md bg-surface-sunken px-2 py-0.5 text-caption font-mono text-text-secondary">
                      {badge}
                    </span>
                  ) : null}
                  <span className="shrink-0 text-caption font-mono text-text-tertiary hidden sm:inline">{m.id}</span>
                  <button
                    type="button"
                    onClick={() => onEditModel(m)}
                    className="p-1.5 rounded-md text-text-tertiary hover:text-text-primary hover:bg-surface-raised"
                    title="编辑"
                  >
                    <Pencil className="w-4 h-4" />
                  </button>
                  <button
                    type="button"
                    onClick={() => onDeleteModel(m)}
                    className="p-1.5 rounded-md text-text-tertiary hover:text-status-error hover:bg-status-error/10"
                    title="删除模型"
                    aria-label={`删除模型 ${m.id}`}
                  >
                    <Trash2 className="w-4 h-4" />
                  </button>
                </div>
              );
            })}
          </div>
          <button
            type="button"
            onClick={onAddModel}
            className="w-full flex items-center justify-center gap-1.5 border border-dashed border-border-strong rounded-lg py-2.5 text-body text-text-secondary hover:bg-surface-raised hover:text-text-primary transition-colors"
          >
            <Plus className="w-4 h-4" />
            添加模型
          </button>
        </ConfigSection>

        <ConfigSection noPadding>
          <p className="text-caption text-text-tertiary flex items-start gap-1.5 px-comfortable py-base">
            <Settings2 className="w-3.5 h-3.5 shrink-0 mt-0.5" />
            API Key 保存在本机 `.agent-work/credentials.local.yaml`；模型配置在 registry.yaml。
          </p>
        </ConfigSection>

        <ConfigSection title="危险操作" hint="不可恢复，请确认没有 Agent 仍引用下列模型">
          <div className="rounded-lg border border-status-error/25 bg-status-error/5 p-4 space-y-3">
            <p className="text-body text-text-secondary">
              删除供应商将移除其下 <strong className="text-text-primary">{group.models.length}</strong> 个模型注册表条目，并清除本机 API Key。
            </p>
            <button
              type="button"
              onClick={onDeleteProvider}
              className="inline-flex items-center gap-1.5 rounded-button border border-status-error/40 px-base py-tight text-body font-medium text-status-error hover:bg-status-error/10 transition-colors"
            >
              <Trash2 className="w-4 h-4" />
              删除供应商及全部模型
            </button>
          </div>
        </ConfigSection>
      </ConfigFormCard>

      {dirty ? (
        <ConfigFooter
          onSave={() => void saveProvider()}
          saving={saving}
          saveDisabled={!label.trim() || !!baseURLError}
          saveLabel="保存配置"
        />
      ) : null}
    </ConfigPanel>
  );
}

function DeleteModelModal({
  entry,
  modelCountInProvider,
  onClose,
  onConfirm,
}: {
  entry: ModelEntry;
  modelCountInProvider: number;
  onClose: () => void;
  onConfirm: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const hint = modelDeleteHint(modelCountInProvider);

  const submit = async () => {
    setBusy(true);
    try {
      await onConfirm();
    } catch (err) {
      toast.error(modelApiError(err, '删除失败'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open onClose={busy ? () => {} : onClose} title="删除模型" width={480}>
      <div className="space-y-base">
        <p className="text-body text-text-secondary">
          即将删除注册表条目 <span className="font-mono text-text-primary">{entry.id}</span>
          {entry.display_name ? (
            <>
              {' '}
             （{entry.display_name}）
            </>
          ) : null}
          。
        </p>
        <div className="rounded-lg border border-border-subtle bg-surface-base px-3 py-2.5 text-caption font-mono text-text-tertiary">
          API 模型名：{entry.model}
        </div>
        {hint ? (
          <p className="text-body text-status-warning bg-status-warning/10 border border-status-warning/20 rounded-lg px-3 py-2.5">
            {hint}
          </p>
        ) : null}
        <div className="flex justify-end gap-snug pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="border border-border-strong rounded-button px-base py-tight text-text-secondary disabled:opacity-50"
          >
            取消
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={busy}
            className="rounded-button px-base py-tight font-medium text-text-inverse bg-status-error hover:bg-status-error/90 disabled:opacity-50"
          >
            {busy ? '删除中…' : '确认删除'}
          </button>
        </div>
      </div>
    </Modal>
  );
}

function DeleteProviderModal({
  group,
  onClose,
  onConfirm,
}: {
  group: ModelProviderGroup;
  onClose: () => void;
  onConfirm: () => Promise<void>;
}) {
  const [confirmText, setConfirmText] = useState('');
  const [busy, setBusy] = useState(false);
  const canDelete = isProviderDeleteConfirmed(group.label, confirmText);

  const submit = async () => {
    if (!canDelete) return;
    setBusy(true);
    try {
      await onConfirm();
    } catch (err) {
      toast.error(modelApiError(err, '删除失败'));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open onClose={busy ? () => {} : onClose} title={`删除供应商 · ${group.label}`} width={520}>
      <div className="space-y-base">
        <p className="text-body text-text-secondary">
          将永久删除以下 {group.models.length} 个模型，并清除本机保存的 API Key。此操作无法撤销。
        </p>
        <ul className="max-h-40 overflow-y-auto rounded-lg border border-border-subtle bg-surface-base divide-y divide-border-subtle">
          {group.models.map((m) => (
            <li key={m.id} className="px-3 py-2 text-body">
              <div className="font-medium text-text-primary">{m.display_name || m.id}</div>
              <div className="text-caption font-mono text-text-tertiary">{m.id}</div>
            </li>
          ))}
        </ul>
        <label className="block">
          <span className="text-body text-text-secondary">
            输入供应商名称 <span className="font-medium text-text-primary">{group.label}</span> 以确认删除
          </span>
          <input
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder={group.label}
            disabled={busy}
            className={`${inputCls} mt-1.5`}
            autoComplete="off"
          />
        </label>
        <div className="flex justify-end gap-snug pt-1">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="border border-border-strong rounded-button px-base py-tight text-text-secondary disabled:opacity-50"
          >
            取消
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={!canDelete || busy}
            className="rounded-button px-base py-tight font-medium text-text-inverse bg-status-error hover:bg-status-error/90 disabled:opacity-50"
          >
            {busy ? '删除中…' : '确认删除'}
          </button>
        </div>
      </div>
    </Modal>
  );
}

function AddModelModal({
  group,
  onClose,
  onSaved,
}: {
  group: ModelProviderGroup;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [modelId, setModelId] = useState('');
  const [modelName, setModelName] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [contextWindow, setContextWindow] = useState('');
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setSaving(true);
    try {
      await createModel({
        id: resolveModelRefId(modelId, modelName, displayName),
        display_name: displayName.trim() || modelId.trim() || modelName.trim(),
        category: group.label,
        provider_id: group.id,
        provider: group.provider,
        api: group.api,
        model: modelName.trim(),
        base_url: group.base_url,
        context_window: contextWindow ? Number(contextWindow) : undefined,
      });
      toast.success('模型已添加');
      onSaved();
    } catch (err) {
      toast.error(modelApiError(err, '添加失败'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer open onClose={onClose} title={`添加模型 · ${group.label}`} width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
          <label className="block">
            <span className="text-body text-text-secondary">注册表 ref（可留空）</span>
            <input
              value={modelId}
              onChange={(e) => setModelId(e.target.value)}
              placeholder="留空则根据 API 模型名自动生成"
              className={`${inputCls} font-mono`}
            />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">API 模型名</span>
            <input value={modelName} onChange={(e) => setModelName(e.target.value)} className={`${inputCls} font-mono`} />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">显示名（可选）</span>
            <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} className={inputCls} />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">上下文窗口（可选）</span>
            <input value={contextWindow} onChange={(e) => setContextWindow(e.target.value)} inputMode="numeric" className={`${inputCls} font-mono`} />
          </label>
          <div className="flex justify-end gap-snug">
            <button onClick={onClose} className="border border-border-strong rounded-button px-base py-tight text-text-secondary">
              取消
            </button>
            <button
              onClick={() => void submit()}
              disabled={!modelName.trim() || saving}
              className="bg-brand-primary text-text-inverse rounded-button px-base py-tight font-medium disabled:opacity-50"
            >
              {saving ? '添加中…' : '添加'}
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}

function EditModelModal({
  entry,
  onClose,
  onSaved,
}: {
  entry: ModelEntry;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [displayName, setDisplayName] = useState(entry.display_name);
  const [modelName, setModelName] = useState(entry.model);
  const [contextWindow, setContextWindow] = useState(entry.context_window ? String(entry.context_window) : '');
  const [maxTokens, setMaxTokens] = useState(entry.max_tokens ? String(entry.max_tokens) : '');
  const [notes, setNotes] = useState(entry.notes ?? '');
  const [saving, setSaving] = useState(false);

  const submit = async () => {
    setSaving(true);
    try {
      await updateModel(
        entry.id,
        buildEditModelPayload(entry, {
          displayName: displayName.trim(),
          model: modelName.trim(),
          contextWindow: contextWindow ? Number(contextWindow) : undefined,
          maxTokens: maxTokens ? Number(maxTokens) : undefined,
          notes: notes.trim(),
        }),
      );
      toast.success(`已保存 ${entry.id}`);
      onSaved();
    } catch (err) {
      toast.error(modelApiError(err, '保存失败'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer open onClose={onClose} title={`编辑模型 · ${entry.id}`} width={480}>
      <div className="p-comfortable">
        <div className="space-y-base">
          <label className="block">
            <span className="text-body text-text-secondary">显示名</span>
            <input value={displayName} onChange={(e) => setDisplayName(e.target.value)} className={inputCls} />
          </label>
          <label className="block">
            <span className="text-body text-text-secondary">API 模型名</span>
            <input value={modelName} onChange={(e) => setModelName(e.target.value)} className={`${inputCls} font-mono`} />
          </label>
          <div className="grid grid-cols-2 gap-snug">
            <label className="block">
              <span className="text-body text-text-secondary">context_window</span>
              <input value={contextWindow} onChange={(e) => setContextWindow(e.target.value)} className={`${inputCls} font-mono`} />
            </label>
            <label className="block">
              <span className="text-body text-text-secondary">max_tokens</span>
              <input value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} className={`${inputCls} font-mono`} />
            </label>
          </div>
          <label className="block">
            <span className="text-body text-text-secondary">备注</span>
            <textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={2} className={`${inputCls} resize-y`} />
          </label>
          <div className="flex justify-end gap-snug">
            <button onClick={onClose} className="border border-border-strong rounded-button px-base py-tight text-text-secondary">
              取消
            </button>
            <button
              onClick={() => void submit()}
              disabled={!displayName.trim() || !modelName.trim() || saving}
              className="bg-brand-primary text-text-inverse rounded-button px-base py-tight font-medium disabled:opacity-50"
            >
              {saving ? '保存中…' : '保存'}
            </button>
          </div>
        </div>
      </div>
    </Drawer>
  );
}
