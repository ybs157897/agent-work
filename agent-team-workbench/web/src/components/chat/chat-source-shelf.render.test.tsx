import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { ChatSourceShelf, chatSourceStatusLabel } from './chat-source-shelf';

describe('ChatSourceShelf', () => {
  it('shows saved and handed-to-Agent states without claiming an unverified read', () => {
    expect(chatSourceStatusLabel('saved')).toBe('原件已保存');
    expect(chatSourceStatusLabel('handed_to_agent')).toBe('已交给 Agent');
    expect(chatSourceStatusLabel('read')).not.toContain('已读取');
    const html = renderToStaticMarkup(
      <ChatSourceShelf
        chatId="wi_1"
        sources={[{
          id: 'src_1', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1',
          filename: '需求.md', mime: 'text/markdown', size: 1024, sha256: 'a'.repeat(64), status: 'handed_to_agent', created_at: '',
        }]}
        loading={false}
        onRetry={() => undefined}
      />,
    );
    expect(html).toContain('本对话资料');
    expect(html).toContain('本对话原件');
    expect(html).toContain('需求.md');
    expect(html).toContain('已交给 Agent');
    expect(html).toContain('下载附件：需求.md');
  });

  it('does not claim saved or offer download when the server reports an unavailable original', () => {
    const html = renderToStaticMarkup(
      <ChatSourceShelf
        chatId="wi_1"
        sources={[{
          id: 'src_missing', workspace_id: 'ws_1', chat_id: 'wi_1', agent_id: 'agent_1',
          filename: '损坏.docx', mime: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document', size: 4, sha256: 'a'.repeat(64), status: 'saved',
          available: false, availability_error: '原件校验失败', created_at: '',
        }]}
        loading={false}
        onRetry={() => undefined}
      />,
    );
    expect(html).toContain('原件不可用：原件校验失败');
    expect(html).not.toContain('原件已保存');
    expect(html).toContain('disabled=""');
  });
});
