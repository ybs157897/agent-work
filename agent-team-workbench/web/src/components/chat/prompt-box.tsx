import {
  AlertCircle,
  Blocks,
  FileText,
  Image as ImageIcon,
  Library,
  Mic,
  MicOff,
  Paperclip,
  SendHorizonal,
  Square,
  X,
} from 'lucide-react';
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type DragEvent,
  type KeyboardEvent,
  type RefObject,
} from 'react';
import type { QueuedMessage } from '../../stores/chat.store';
import { useAgentsStore } from '../../stores/agents.store';
import { formatBytes } from '../../utils/artifact-visuals';
import { isChatAgent } from '../../utils/agent-scope';
import { activeMention, applyMention, mentionableAgents, type MentionState } from '../../utils/mention';
import { Avatar } from '../avatar';
import { Button } from '../ui';
import { validatePromptFiles, type PromptFileDescriptor } from './prompt-files';
import { fileFromChatAttachment, loadChatAttachments, removeChatAttachment, saveChatAttachments, type ChatAttachmentRecoveryScope } from '../../utils/chat-attachment-recovery';

export const PROMPT_LIBRARY = [
  { title: '关键指标', prompt: '请先给出结论，再用关键指标卡展示最重要的数据，并注明数据来源。' },
  { title: '表格对比', prompt: '请将核心差异整理成结构清晰的对比表格，并在表格后给出建议。' },
  { title: '趋势图', prompt: '请用趋势图展示数据变化，同时提供可核对的数据表和简短结论。' },
] as const;

const FILE_ACCEPT = '.txt,.md,.markdown,.csv,.json,.pdf,.doc,.docx,.ppt,.pptx,.png,.jpg,.jpeg,.webp,text/plain,text/markdown,text/csv,application/json,application/pdf,application/msword,application/vnd.ms-powerpoint,application/vnd.openxmlformats-officedocument.wordprocessingml.document,application/vnd.openxmlformats-officedocument.presentationml.presentation,image/png,image/jpeg,image/webp';
const IMAGE_ACCEPT = '.png,.jpg,.jpeg,.webp,image/png,image/jpeg,image/webp';

function attachmentScopeKey(scope: ChatAttachmentRecoveryScope): string {
  return `${scope.workspaceId}:${scope.agentId}:${scope.conversationId ?? 'new'}`;
}

export interface PromptAttachment extends PromptFileDescriptor {
  file: File;
  previewUrl?: string;
}

interface SpeechResultLike {
  resultIndex?: number;
  results: ArrayLike<{ 0?: { transcript?: string } }>;
}

interface SpeechErrorLike {
  error?: string;
}

interface SpeechRecognitionLike {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((event: SpeechResultLike) => void) | null;
  onerror: ((event: SpeechErrorLike) => void) | null;
  onend: (() => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
}

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike;

export interface PromptBoxProps {
  draft: string;
  onDraftChange: (value: string) => void;
  onSend: () => void | Promise<boolean>;
  placeholder: string;
  inputRef: RefObject<HTMLTextAreaElement>;
  queue: readonly QueuedMessage[];
  onRemoveQueued: (index: number) => void;
  canDrainQueue: boolean;
  onDrainQueue: () => void;
  sending: boolean;
  runInFlight: boolean;
  stopping: boolean;
  onStop: () => void;
  usageText: string | null;
  /** 仅 Task 编排入口开启 @Agent；独立 Chat 默认不提供调度候选。 */
  mentionsEnabled?: boolean;
  /** U01: local-only attachments are recovered by the same Workspace/Agent/conversation scope. */
  attachmentScope?: ChatAttachmentRecoveryScope;
  /** Keep the selected originals while a first Chat is being materialized. */
  preserveAttachmentsOnScopeChange?: boolean;
  /** Parent-triggered cleanup after a non-composer action delivered the originals. */
  clearAttachmentsRequest?: number;
  onAttachmentsChange?: (attachments: readonly PromptAttachment[]) => void;
}

export function PromptBox({
  draft,
  onDraftChange,
  onSend,
  placeholder,
  inputRef,
  queue,
  onRemoveQueued,
  canDrainQueue,
  onDrainQueue,
  sending,
  runInFlight,
  stopping,
  onStop,
  usageText,
  mentionsEnabled = false,
  attachmentScope,
  preserveAttachmentsOnScopeChange = false,
  clearAttachmentsRequest = 0,
  onAttachmentsChange,
}: PromptBoxProps) {
  const [attachments, setAttachments] = useState<PromptAttachment[]>([]);
  const [fileError, setFileError] = useState('');
  const [dragging, setDragging] = useState(false);
  const [appsOpen, setAppsOpen] = useState(false);
  const [speechSupported, setSpeechSupported] = useState(false);
  const [listening, setListening] = useState(false);
  const [speechError, setSpeechError] = useState('');
  // @ 提及弹层：mention = 光标处提及态（null 关闭）；mentionIndex = 键盘高亮行。
  const [mention, setMention] = useState<MentionState | null>(null);
  const [mentionIndex, setMentionIndex] = useState(0);
  const allAgents = useAgentsStore((s) => s.agents);
  const agents = useMemo(
    () => (mentionsEnabled ? allAgents.filter(isChatAgent) : []),
    [allAgents, mentionsEnabled],
  );
  const fileInputRef = useRef<HTMLInputElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const appsRef = useRef<HTMLDivElement>(null);
  const dragDepth = useRef(0);
  const recognitionRef = useRef<SpeechRecognitionLike | null>(null);
  const attachmentsRef = useRef(attachments);
  const draftRef = useRef(draft);
  const sendInFlightRef = useRef(false);
  const scopeKey = attachmentScope
    ? `${attachmentScope.workspaceId}:${attachmentScope.agentId}:${attachmentScope.conversationId ?? 'new'}`
    : '';
  const previousScopeKeyRef = useRef(scopeKey);
  const skippedScopeKeyRef = useRef<string | null>(null);
  const persistedScopeRef = useRef<ChatAttachmentRecoveryScope | undefined>(attachmentScope);
  const attachmentScopesRef = useRef(new Map<string, ChatAttachmentRecoveryScope[]>());
  const attachmentPersistenceRef = useRef<Promise<void> | null>(null);
  const clearAttachmentsRequestRef = useRef(clearAttachmentsRequest);

  const rememberAttachmentScope = useCallback((key: string, scope: ChatAttachmentRecoveryScope) => {
    const scopes = attachmentScopesRef.current.get(key) ?? [];
    if (!scopes.some((item) => attachmentScopeKey(item) === attachmentScopeKey(scope))) scopes.push(scope);
    attachmentScopesRef.current.set(key, scopes);
  }, []);
  const scopesForAttachment = useCallback((key: string): ChatAttachmentRecoveryScope[] => {
    const remembered = attachmentScopesRef.current.get(key);
    if (remembered?.length) return remembered;
    const fallback = persistedScopeRef.current ?? attachmentScope;
    return fallback ? [fallback] : [];
  }, [attachmentScope]);

  const persistAttachments = (scope: ChatAttachmentRecoveryScope, records: readonly PromptAttachment[]): Promise<void> => {
    const previous = attachmentPersistenceRef.current ?? Promise.resolve();
    const next = previous
      .catch(() => undefined)
      .then(() => saveChatAttachments(scope, records.map((attachment) => ({
        key: attachment.key,
        name: attachment.name,
        size: attachment.size,
        mime: attachment.mime,
        kind: attachment.kind,
        lastModified: attachment.file.lastModified,
        blob: attachment.file,
      }))));
    attachmentPersistenceRef.current = next;
    void next.catch((error: unknown) => setFileError(error instanceof Error ? `附件暂存失败：${error.message}` : '附件暂存失败，请复制文件后重试'));
    return next;
  };

  useEffect(() => {
    if (!attachmentScope?.workspaceId || !attachmentScope.agentId) {
      setAttachments([]);
      return;
    }
    const scopeChanged = previousScopeKeyRef.current !== scopeKey;
    previousScopeKeyRef.current = scopeKey;
    if (scopeChanged && preserveAttachmentsOnScopeChange) {
      const selected = attachmentsRef.current;
      const previousScope = persistedScopeRef.current;
      persistedScopeRef.current = attachmentScope;
      if (selected.length) {
        selected.forEach((attachment) => rememberAttachmentScope(attachment.key, attachmentScope));
        const migrated = persistAttachments(attachmentScope, selected);
        if (previousScope && attachmentScopeKey(previousScope) !== attachmentScopeKey(attachmentScope)) {
          void migrated
            .then(() => Promise.all(selected.map((attachment) => removeChatAttachment(previousScope, attachment.key).catch(() => undefined))))
            .catch(() => undefined);
        }
      }
      skippedScopeKeyRef.current = scopeKey;
      return;
    }
    if (skippedScopeKeyRef.current === scopeKey) return;
    skippedScopeKeyRef.current = null;
    persistedScopeRef.current = attachmentScope;
    let active = true;
    void loadChatAttachments(attachmentScope)
      .then((records) => {
        if (!active) return;
        records.forEach((record) => rememberAttachmentScope(record.key, attachmentScope));
        setAttachments(records.map((record) => ({
          key: record.key,
          name: record.name,
          size: record.size,
          mime: record.mime,
          kind: record.kind,
          sourceIndex: 0,
          file: fileFromChatAttachment(record),
          ...(record.kind === 'image' && typeof URL !== 'undefined' ? { previewUrl: URL.createObjectURL(record.blob) } : {}),
        })));
      })
      .catch((error: unknown) => {
        if (active) setFileError(error instanceof Error ? `附件恢复失败：${error.message}` : '附件恢复失败，请重新选择原文件');
      });
    return () => { active = false; };
  }, [attachmentScope, preserveAttachmentsOnScopeChange, rememberAttachmentScope, scopeKey]);

  useEffect(() => {
    setSpeechSupported(getSpeechConstructor() !== null);
    return () => {
      recognitionRef.current?.abort();
      for (const attachment of attachmentsRef.current) {
        if (attachment.previewUrl) URL.revokeObjectURL(attachment.previewUrl);
      }
    };
  }, []);

  useEffect(() => {
    attachmentsRef.current = attachments;
    onAttachmentsChange?.(attachments);
  }, [attachments, onAttachmentsChange]);

  useEffect(() => {
    draftRef.current = draft;
  }, [draft]);

  // 提及候选：只列能被服务端 @直达 命中的名字（无空白），按已输入 query 过滤。
  const mentionAgents = mentionsEnabled ? mentionableAgents(agents) : [];
  const filteredAgents = mention
    ? mentionAgents.filter((a) => a.name.toLowerCase().includes(mention.query.toLowerCase()))
    : [];
  const mentionOpen = mention !== null && filteredAgents.length > 0;
  const activeMentionId = mentionOpen
    ? `chat-mention-option-${filteredAgents[Math.min(mentionIndex, filteredAgents.length - 1)].id}`
    : undefined;

  // 草稿被整段替换（语音转写 / Library 提示词）时重算提及态，防弹层锚点失真。
  useEffect(() => {
    if (!mention) return;
    const textarea = inputRef.current;
    const caret = textarea?.selectionStart ?? draft.length;
    if (!activeMention(draft, caret)) setMention(null);
  }, [draft, mention, inputRef]);

  useEffect(() => {
    const textarea = inputRef.current;
    if (!textarea) return;
    textarea.style.height = 'auto';
    textarea.style.height = `${Math.min(textarea.scrollHeight, 160)}px`;
  }, [draft, inputRef]);

  useEffect(() => {
    if (!appsOpen) return;
    const closeOnOutside = (event: MouseEvent) => {
      if (!appsRef.current?.contains(event.target as Node)) setAppsOpen(false);
    };
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') setAppsOpen(false);
    };
    document.addEventListener('mousedown', closeOnOutside);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('mousedown', closeOnOutside);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [appsOpen]);

  const addFiles = useCallback((selected: readonly File[]) => {
    const result = validatePromptFiles(selected, attachmentsRef.current);
    const next = result.accepted.map((descriptor) => {
      const file = selected[descriptor.sourceIndex];
      const previewUrl = descriptor.kind === 'image' && typeof URL !== 'undefined' && typeof URL.createObjectURL === 'function'
        ? URL.createObjectURL(file)
        : undefined;
      return { ...descriptor, file, ...(previewUrl ? { previewUrl } : {}) };
    });
    if (next.length) {
      const merged = [...attachmentsRef.current, ...next];
      setAttachments(merged);
      persistedScopeRef.current = attachmentScope;
      if (attachmentScope) {
        next.forEach((attachment) => rememberAttachmentScope(attachment.key, attachmentScope));
        persistAttachments(attachmentScope, merged);
      }
    }
    setFileError(result.errors.join('；'));
  }, [attachmentScope, rememberAttachmentScope]);

  const selectFiles = (event: ChangeEvent<HTMLInputElement>) => {
    addFiles(Array.from(event.target.files ?? []));
    event.target.value = '';
  };

  const removeAttachment = (key: string) => {
    setAttachments((current) => {
      const target = current.find((attachment) => attachment.key === key);
      if (target?.previewUrl) URL.revokeObjectURL(target.previewUrl);
      return current.filter((attachment) => attachment.key !== key);
    });
    const scopes = scopesForAttachment(key);
    if (scopes.length) {
      const previous = attachmentPersistenceRef.current ?? Promise.resolve();
      const next = previous.catch(() => undefined).then(() => Promise.all(scopes.map((scope) => removeChatAttachment(scope, key).catch(() => undefined))).then(() => undefined));
      attachmentPersistenceRef.current = next;
      void next.catch(() => setFileError('附件从本地恢复区移除失败，请刷新后重试'));
    }
    attachmentScopesRef.current.delete(key);
    setFileError('');
  };

  useEffect(() => {
    if (clearAttachmentsRequestRef.current === clearAttachmentsRequest) return;
    clearAttachmentsRequestRef.current = clearAttachmentsRequest;
    const selected = attachmentsRef.current;
    if (selected.length === 0) return;
    void (async () => {
      await attachmentPersistenceRef.current?.catch(() => undefined);
      await Promise.all(selected.flatMap((attachment) => scopesForAttachment(attachment.key).map((scope) => removeChatAttachment(scope, attachment.key).catch(() => undefined))));
      for (const attachment of selected) {
        if (attachment.previewUrl) URL.revokeObjectURL(attachment.previewUrl);
        attachmentScopesRef.current.delete(attachment.key);
      }
      setAttachments([]);
    })();
  }, [clearAttachmentsRequest, scopesForAttachment]);

  const onDragEnter = (event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes('Files')) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };

  const onDragLeave = (event: DragEvent<HTMLDivElement>) => {
    event.preventDefault();
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };

  const onDrop = (event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes('Files')) return;
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    addFiles(Array.from(event.dataTransfer.files));
  };

  const updateMention = (value: string, caret: number | null) => {
    if (!mentionsEnabled) {
      setMention(null);
      return;
    }
    setMention(activeMention(value, caret ?? value.length));
    setMentionIndex(0);
  };

  // 光标在文本内移动（方向键 / 点击）时重算提及态，弹层随之开合。
  const syncMentionFromDom = () => {
    if (!mentionsEnabled) {
      setMention(null);
      return;
    }
    const textarea = inputRef.current;
    if (!textarea) return;
    setMention(activeMention(textarea.value, textarea.selectionStart ?? textarea.value.length));
  };

  const selectMention = (name: string) => {
    if (!mention) return;
    const textarea = inputRef.current;
    const caret = textarea?.selectionStart ?? draft.length;
    const insertion = applyMention(draft, mention, caret, name);
    onDraftChange(insertion.text);
    setMention(null);
    if (textarea) {
      // 受控 value 落 DOM 后把光标归位到插入词尾。
      requestAnimationFrame(() => {
        textarea.focus();
        textarea.setSelectionRange(insertion.caret, insertion.caret);
      });
    }
  };

  const submit = async () => {
    if (sendInFlightRef.current || sending) return;
    sendInFlightRef.current = true;
    const selected = attachmentsRef.current;
    try {
      const retained = await onSend();
      if (retained === true && selected.length > 0) {
        await attachmentPersistenceRef.current?.catch(() => undefined);
        await Promise.all(selected.flatMap((attachment) => scopesForAttachment(attachment.key).map((scope) => removeChatAttachment(scope, attachment.key).catch(() => undefined))));
        for (const attachment of selected) {
          if (attachment.previewUrl) URL.revokeObjectURL(attachment.previewUrl);
          attachmentScopesRef.current.delete(attachment.key);
        }
        setAttachments([]);
      }
    } finally {
      sendInFlightRef.current = false;
    }
  };

  const onInputKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing || event.keyCode === 229) return;
    if (mentionOpen) {
      if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
        event.preventDefault();
        const delta = event.key === 'ArrowDown' ? 1 : -1;
        setMentionIndex((index) => Math.min(Math.max(index + delta, 0), filteredAgents.length - 1));
        return;
      }
      if (event.key === 'Escape') {
        setMention(null);
        return;
      }
      if (event.key === 'Enter' || event.key === 'Tab') {
        event.preventDefault();
        selectMention(filteredAgents[Math.min(mentionIndex, filteredAgents.length - 1)].name);
        return;
      }
    }
    if (event.key !== 'Enter' || event.shiftKey) return;
    event.preventDefault();
    void submit();
  };

  const toggleSpeech = () => {
    if (listening) {
      recognitionRef.current?.stop();
      return;
    }
    const Constructor = getSpeechConstructor();
    if (!Constructor) return;
    setSpeechError('');
    const recognition = new Constructor();
    recognition.continuous = false;
    recognition.interimResults = false;
    recognition.lang = 'zh-CN';
    recognition.onresult = (event) => {
      let transcript = '';
      for (let index = event.resultIndex ?? 0; index < event.results.length; index += 1) {
        transcript += event.results[index][0]?.transcript ?? '';
      }
      const value = transcript.trim();
      if (value) onDraftChange([draftRef.current.trimEnd(), value].filter(Boolean).join('\n'));
    };
    recognition.onerror = (event) => {
      setSpeechError(speechErrorMessage(event.error));
      setListening(false);
    };
    recognition.onend = () => {
      setListening(false);
      recognitionRef.current = null;
    };
    recognitionRef.current = recognition;
    try {
      recognition.start();
      setListening(true);
    } catch {
      recognitionRef.current = null;
      setSpeechError('无法启动语音输入，请检查浏览器权限');
    }
  };

  const hasAttachments = attachments.length > 0;
  const sendDisabled = (!draft.trim() && !hasAttachments) || sending;

  return (
    <div
      className={`chat-composer chat-prompt-box${dragging ? ' chat-prompt-box-dragging' : ''}`}
      data-chat-composer
      onDragEnter={onDragEnter}
      onDragOver={(event) => {
        if (event.dataTransfer.types.includes('Files')) event.preventDefault();
      }}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <input ref={fileInputRef} type="file" className="hidden" multiple accept={FILE_ACCEPT} aria-label="选择附件" onChange={selectFiles} />
      <input ref={imageInputRef} type="file" className="hidden" multiple accept={IMAGE_ACCEPT} aria-label="选择图片" onChange={selectFiles} />
      {dragging && <div className="chat-prompt-drop-overlay" role="status">松开以添加附件</div>}
      {queue.length > 0 && (
        <div className="chat-composer-queue">
          <div className="flex items-center justify-between gap-2">
            <span className="text-caption text-text-tertiary">待发送队列（{queue.length} 条）</span>
            {canDrainQueue && (
              <button type="button" onClick={onDrainQueue} disabled={sending} className="text-caption font-medium text-brand-primary transition-colors hover:text-brand-accent disabled:cursor-not-allowed disabled:opacity-50">
                继续发送
              </button>
            )}
          </div>
          {queue.map((item, index) => (
            <div key={item.clientKey} className="flex items-center gap-2">
              <span className="text-caption tabular-nums text-text-tertiary">{index + 1}</span>
              <span className="min-w-0 flex-1 truncate text-caption text-text-secondary">{item.text}</span>
              <button type="button" aria-label={`移除第 ${index + 1} 条待发送消息`} title="移除" onClick={() => onRemoveQueued(index)} className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-button text-text-tertiary transition-colors hover:bg-surface-sunken hover:text-text-primary">
                <X className="h-3.5 w-3.5" />
              </button>
            </div>
          ))}
        </div>
      )}
      <textarea
        ref={inputRef}
        value={draft}
        onChange={(event) => {
          onDraftChange(event.target.value);
          updateMention(event.target.value, event.target.selectionStart);
        }}
        onSelect={syncMentionFromDom}
        onBlur={() => setMention(null)}
        onKeyDown={onInputKeyDown}
        rows={1}
        placeholder={placeholder}
        aria-label="输入消息"
        aria-haspopup={mentionsEnabled ? 'listbox' : undefined}
        aria-expanded={mentionsEnabled ? mentionOpen : undefined}
        aria-activedescendant={mentionsEnabled ? activeMentionId : undefined}
        className="chat-composer-input"
      />
      {mentionOpen && (
        <div
          className="absolute bottom-full left-0 right-0 z-40 mb-2 overflow-hidden rounded-card border border-border-subtle bg-surface-raised shadow-level-3"
          role="listbox"
          aria-label="提及智能体"
        >
          <ul className="max-h-64 overflow-y-auto py-micro">
            {filteredAgents.map((agent, index) => (
              <li key={agent.id}>
                <button
                  type="button"
                  role="option"
                  id={`chat-mention-option-${agent.id}`}
                  aria-selected={index === mentionIndex}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => selectMention(agent.name)}
                  onMouseEnter={() => setMentionIndex(index)}
                  className={`flex w-full items-center gap-2 px-snug py-tight text-left transition-colors ${
                    index === mentionIndex ? 'bg-brand-primary/10' : 'hover:bg-surface-base'
                  }`}
                >
                  <Avatar name={agent.name} url={agent.avatar} size={22} />
                  <span className="shrink-0 text-body text-text-primary">{agent.name}</span>
                  <span className="min-w-0 flex-1 truncate text-caption text-text-tertiary">{agent.role}</span>
                </button>
              </li>
            ))}
          </ul>
          <div className="border-t border-border-subtle bg-surface-sunken/55 px-snug py-micro text-caption text-text-tertiary">
            ↑↓ 选择，Enter 确认，Esc 关闭；@名字 需位于消息开头才会直达该 agent
          </div>
        </div>
      )}
      {attachments.length > 0 && (
        <ul className="chat-prompt-attachments" aria-label="待处理附件">
          {attachments.map((attachment) => (
            <li key={attachment.key} className="chat-prompt-attachment">
              {attachment.previewUrl ? (
                <img src={attachment.previewUrl} alt="" className="chat-prompt-attachment-preview" />
              ) : (
                <span className="chat-prompt-attachment-icon" aria-hidden><FileText className="h-4 w-4" /></span>
              )}
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium text-text-primary" title={attachment.name}>{attachment.name}</span>
                <span className="block text-caption text-text-tertiary">{formatBytes(attachment.size)}</span>
              </span>
              <button type="button" className="chat-prompt-attachment-remove" onClick={() => removeAttachment(attachment.key)} aria-label={`移除附件：${attachment.name}`} title="移除附件">
                <X className="h-3.5 w-3.5" />
              </button>
            </li>
          ))}
        </ul>
      )}
      {(fileError || hasAttachments || speechError) && (
        <div className="chat-prompt-feedback" role={fileError || speechError ? 'alert' : 'status'}>
          <AlertCircle className="h-3.5 w-3.5 shrink-0" aria-hidden />
          <span>{fileError || speechError || '附件已在本地暂存，尚未提交；发送时会把原件交给当前对话。'}</span>
        </div>
      )}
      <div className="chat-prompt-footer">
        <div className="chat-prompt-tools" role="toolbar" aria-label="输入工具">
          <button type="button" className="chat-prompt-tool" onClick={() => fileInputRef.current?.click()} aria-label="添加附件" title="添加附件">
            <Paperclip className="h-4 w-4" />
          </button>
          <button type="button" className="chat-prompt-tool" onClick={() => imageInputRef.current?.click()} aria-label="添加图片" title="添加图片">
            <ImageIcon className="h-4 w-4" />
          </button>
          <button
            type="button"
            className={`chat-prompt-tool${listening ? ' chat-prompt-tool-active' : ''}`}
            onClick={toggleSpeech}
            disabled={!speechSupported}
            aria-label={listening ? '停止语音输入' : '开始语音输入'}
            aria-pressed={listening}
            title={speechSupported ? (listening ? '停止语音输入' : '语音转文字') : '当前浏览器不支持语音转文字'}
          >
            {speechSupported ? <Mic className="h-4 w-4" /> : <MicOff className="h-4 w-4" />}
          </button>
          <div ref={appsRef} className="relative">
            <button type="button" className={`chat-prompt-tool${appsOpen ? ' chat-prompt-tool-active' : ''}`} onClick={() => setAppsOpen((value) => !value)} aria-label="打开 Library 与 Apps" aria-haspopup="dialog" aria-expanded={appsOpen} title="Library 与 Apps">
              <Blocks className="h-4 w-4" />
            </button>
            {appsOpen && (
              <div className="chat-prompt-apps" role="dialog" aria-label="Library 与 Apps">
                <div className="chat-prompt-apps-head"><Library className="h-4 w-4" aria-hidden />Prompt Library</div>
                <div className="chat-prompt-library">
                  {PROMPT_LIBRARY.map((item) => (
                    <button key={item.title} type="button" onClick={() => {
                      onDraftChange(item.prompt);
                      setAppsOpen(false);
                      inputRef.current?.focus();
                    }}>
                      <span>{item.title}</span><small>{item.prompt}</small>
                    </button>
                  ))}
                </div>
                <div className="chat-prompt-app-status"><span>LanguageGUI v1</span><b className="chat-prompt-app-status-enabled">已启用</b></div>
                <div className="chat-prompt-app-status"><span>外部 Apps</span><b>尚未配置</b></div>
              </div>
            )}
          </div>
        </div>
        <span className="min-w-0 flex-1 truncate text-caption tabular-nums text-text-tertiary">{usageText}</span>
        {runInFlight && (
          <Button variant="ghost" type="button" onClick={onStop} disabled={stopping} aria-label={stopping ? '停止中' : '停止生成'}>
            <Square className="h-3.5 w-3.5" />
            {stopping ? '停止中' : '停止'}
          </Button>
        )}
        <Button variant="primary" type="button" onClick={() => void submit()} disabled={sendDisabled} aria-busy={sending} aria-label={runInFlight ? '加入发送队列' : '发送消息'}>
          {sending ? '发送中' : runInFlight ? '加入队列' : '发送'}
          <SendHorizonal className="h-4 w-4" />
        </Button>
      </div>
    </div>
  );
}

function getSpeechConstructor(): SpeechRecognitionConstructor | null {
  if (typeof window === 'undefined') return null;
  const candidate = window as typeof window & {
    SpeechRecognition?: SpeechRecognitionConstructor;
    webkitSpeechRecognition?: SpeechRecognitionConstructor;
  };
  return candidate.SpeechRecognition ?? candidate.webkitSpeechRecognition ?? null;
}

function speechErrorMessage(error?: string): string {
  switch (error) {
    case 'not-allowed':
    case 'service-not-allowed':
      return '麦克风权限被拒绝，仍可继续输入文字';
    case 'audio-capture':
      return '没有可用的麦克风，仍可继续输入文字';
    case 'network':
      return '语音识别网络不可用，仍可继续输入文字';
    default:
      return '语音输入失败，仍可继续输入文字';
  }
}
