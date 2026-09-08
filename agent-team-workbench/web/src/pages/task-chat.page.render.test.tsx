import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router-dom';
import { TaskIntakeQuestionGroup } from '../components/chat/task-intake-question-card';
import TaskChatPage, { TaskDraftCard, TaskIntakeModelSelector, TaskIntakePublicationReceiptCard } from './task-chat.page';

describe('TaskChatPage', () => {
  it('renders an independent full-height task chat page without a Drawer', () => {
    const html = renderToStaticMarkup(<TaskChatPage />);
    expect(html).toContain('data-task-chat="true"');
    expect(html).toContain('>任务对话</h1>');
    expect(html).toContain('chat-composer');
    expect(html).not.toContain('role="dialog"');
  });

  it('keeps the existing task draft and question surfaces in the independent page', () => {
    const question = { id: 'scope', title: '覆盖哪些端？', options: ['Web', '移动端'], multiple: true };
    const html = renderToStaticMarkup(
      <div className="chat-languagegui-skin">
        <TaskIntakeQuestionGroup
          messageId="assistant-1"
          questions={[question]}
          answers={{}}
          onSelection={() => undefined}
          onSupplement={() => undefined}
          onSubmit={() => undefined}
        />
        <TaskDraftCard
          draft={{ title: '完善登录', description: '描述', acceptance_criteria: ['可以验收'] }}
          priority="medium"
          dueDate=""
          ready
          disabled={false}
          pending={false}
          publishing={false}
          onFieldChange={() => undefined}
          onPriorityChange={() => undefined}
          onDueDateChange={() => undefined}
          onSubmit={() => undefined}
        />
        <TaskIntakeModelSelector models={[]} loading={false} error={null} value="" disabled={false} onChange={() => undefined} compact />
      </div>,
    );
    expect(html).toContain('type="checkbox"');
    expect(html).toContain('任务草案');
    expect(html).toContain('使用任务默认模型');
  });

  it('shows a published receipt with a task link and explicit new conversation action', () => {
    const html = renderToStaticMarkup(<MemoryRouter><TaskIntakePublicationReceiptCard
      receipt={{ workspaceId: 'ws_1', workItemId: 'wi_1', title: '完善登录', publishedAt: '2026-09-05T00:00:00Z' }}
      onStartNew={() => undefined}
    /></MemoryRouter>);
    expect(html).toContain('任务已发布');
    expect(html).toContain('href="/tasks/wi_1"');
    expect(html).toContain('查看任务');
    expect(html).toContain('开始新的任务对话');
  });
});
