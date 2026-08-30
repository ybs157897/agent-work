import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { PROMPT_LIBRARY, PromptBox } from './prompt-box';

const noop = () => undefined;

describe('PromptBox', () => {
  it('renders the LanguageGUI input tools, queue, stop and enqueue states with accessible names', () => {
    const html = renderToStaticMarkup(
      <PromptBox
        draft="继续检查"
        onDraftChange={noop}
        onSend={noop}
        placeholder="输入消息"
        inputRef={{ current: null }}
        queue={[{ text: '第一条', clientKey: 'q:1' }]}
        onRemoveQueued={noop}
        canDrainQueue
        onDrainQueue={noop}
        sending={false}
        runInFlight
        stopping={false}
        onStop={noop}
        usageText="12k tokens"
      />,
    );

    expect(html).toContain('data-chat-composer="true"');
    expect(html).toContain('aria-label="输入消息"');
    expect(html).toContain('aria-label="选择附件"');
    expect(html).toContain('aria-label="选择图片"');
    expect(html).toContain('role="toolbar" aria-label="输入工具"');
    expect(html).toContain('aria-label="添加附件"');
    expect(html).toContain('aria-label="添加图片"');
    expect(html).toContain('aria-label="开始语音输入"');
    expect(html).toContain('当前浏览器不支持语音转文字');
    expect(html).toContain('aria-label="打开 Library 与 Apps"');
    expect(html).toContain('aria-haspopup="dialog"');
    expect(html).toContain('待发送队列（1 条）');
    expect(html).toContain('aria-label="移除第 1 条待发送消息"');
    expect(html).toContain('aria-label="停止生成"');
    expect(html).toContain('aria-label="加入发送队列"');
    expect(html).toContain('加入队列');
    expect(html).toContain('12k tokens');
    expect(html).not.toContain('提及智能体');
  });

  it('keeps an empty idle composer send-disabled and ships useful Library prompts', () => {
    const html = renderToStaticMarkup(
      <PromptBox
        draft=""
        onDraftChange={noop}
        onSend={noop}
        placeholder="输入消息"
        inputRef={{ current: null }}
        queue={[]}
        onRemoveQueued={noop}
        canDrainQueue={false}
        onDrainQueue={noop}
        sending={false}
        runInFlight={false}
        stopping={false}
        onStop={noop}
        usageText={null}
      />,
    );
    expect(html).toContain('aria-label="发送消息"');
    expect(html).toContain('disabled=""');
    expect(PROMPT_LIBRARY).toHaveLength(3);
    expect(PROMPT_LIBRARY.every((item) => item.prompt.length > item.title.length)).toBe(true);
  });
});
