import { renderToStaticMarkup } from 'react-dom/server';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AgentProfile } from '../api/types';
import { useAgentsStore } from '../stores/agents.store';
import { LayoutShell } from './layout-shell';

// 静态渲染取不到 zustand 的 SSR 快照，按仓内既有做法让 hook 直接读当前 state。
vi.mock('../stores/agents.store', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../stores/agents.store')>();
  return { ...actual, useAgentsStore: Object.assign((selector: (state: ReturnType<typeof actual.useAgentsStore.getState>) => unknown) => selector(actual.useAgentsStore.getState()), actual.useAgentsStore) };
});

const agent = (id: string, slug: string, name: string): AgentProfile => ({
  id,
  slug,
  name,
  role: 'pm',
  skills: [],
  availability: 'enabled',
  presence: 'idle',
  version: 1,
});

const ATLAS = agent('agent_atlas', 'atlas', '产品智能体');
const FORGE = agent('agent_forge', 'forge', '开发智能体');

function renderShell(path: string) {
  return renderToStaticMarkup(
    <MemoryRouter initialEntries={[path]}>
      <LayoutShell><section aria-label="页面正文">正文</section></LayoutShell>
    </MemoryRouter>,
  );
}

afterEach(() => {
  useAgentsStore.setState({ agents: [], selectedAgentId: null });
});

describe('global chat navigation', () => {
  it('uses the existing Chat destination as the single conversation entry', () => {
    useAgentsStore.setState({ agents: [ATLAS, FORGE] });
    const html = renderShell('/chat');
    expect(html).toMatch(/<a(?=[^>]*href="\/chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('对话');
    expect(html).not.toContain('任务对话');
    expect(html).toContain('href="/tasks"');
    expect(html).toContain('href="/chat"');
    expect(html).toMatch(/<main(?=[^>]*id="main-content")(?=[^>]*class="[^"]*workbench-chat-surface)[^>]*>/);
    expect(html).not.toContain('<header');
    expect(html).not.toContain('role="dialog"');
    expect(html).not.toContain('打开主导航');
    expect(html).not.toContain('关闭主导航');
    expect(html).toContain('data-theme="light"');
  });

  it('keeps the legacy task-chat URL inside the same full-height shell for redirect recovery', () => {
    const html = renderShell('/task-chat');
    expect(html).not.toMatch(/<a(?=[^>]*href="\/task-chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('href="/chat"');
    expect(html).toContain('workbench-chat-surface');
  });

  it('keeps the board as a separate destination', () => {
    const html = renderShell('/tasks');
    expect(html).toMatch(/<a(?=[^>]*href="\/tasks")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).not.toMatch(/<a(?=[^>]*href="\/task-chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('plane-board-shell');
  });

  it('exposes the knowledge librarian as a first-class workspace destination', () => {
    const html = renderShell('/library');
    expect(html).toMatch(/<a(?=[^>]*href="\/library")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toContain('知识库');
    expect(html).toContain('workbench-main-surface');
    expect(html).not.toContain('mesh-bg');
  });
});

describe('featured agent entries', () => {
  it('places the roster agents right after 对话 as deep links labelled with their display names', () => {
    useAgentsStore.setState({ agents: [ATLAS, FORGE] });
    const html = renderShell('/chat');
    const positions = [
      html.indexOf('href="/chat?agent=agent_atlas"'),
      html.indexOf('href="/chat?agent=agent_forge"'),
    ];
    expect(positions[0]).toBeGreaterThan(html.indexOf('href="/chat"'));
    expect(positions[1]).toBeGreaterThan(positions[0]);
    expect(positions[1]).toBeLessThan(html.indexOf('href="/models"'));
    expect(html).toMatch(/<a(?=[^>]*href="\/chat\?agent=agent_atlas")[^>]*>.*?产品智能体/s);
    expect(html).toMatch(/<a(?=[^>]*href="\/chat\?agent=agent_forge")[^>]*>.*?开发智能体/s);
  });

  it('omits an entry whose slug is missing from the roster', () => {
    useAgentsStore.setState({ agents: [ATLAS] });
    const html = renderShell('/chat');
    expect(html).toContain('href="/chat?agent=agent_atlas"');
    expect(html).not.toContain('agent_forge');
    expect(html).not.toContain('开发智能体');
  });

  it('renders no agent entry when the roster is empty', () => {
    const html = renderShell('/chat');
    expect(html).not.toContain('?agent=');
  });

  it('omits a disabled agent entry because it can no longer start a run', () => {
    useAgentsStore.setState({ agents: [{ ...ATLAS, availability: 'disabled' }, FORGE] });
    const html = renderShell('/chat');
    expect(html).not.toContain('agent_atlas');
    expect(html).not.toContain('产品智能体');
    expect(html).toContain('href="/chat?agent=agent_forge"');
  });

  it('activates the routed agent entry and leaves 对话 without a second highlight', () => {
    useAgentsStore.setState({ agents: [ATLAS, FORGE] });
    const html = renderShell('/chat?agent=agent_atlas');
    expect(html).toMatch(/<a(?=[^>]*href="\/chat\?agent=agent_atlas")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toMatch(/<a(?=[^>]*href="\/chat\?agent=agent_forge")(?![^>]*aria-current)[^>]*>/);
    expect(html).not.toMatch(/<a(?=[^>]*href="\/chat")(?=[^>]*aria-current="page")[^>]*>/);
  });

  it('keeps 对话 highlighted for a conversation outside the featured roster', () => {
    useAgentsStore.setState({ agents: [ATLAS, FORGE] });
    const html = renderShell('/chat?agent=agent_knowledge_librarian_ws_1');
    expect(html).toMatch(/<a(?=[^>]*href="\/chat")(?=[^>]*aria-current="page")[^>]*>/);
    expect(html).toMatch(/<a(?=[^>]*href="\/chat\?agent=agent_atlas")(?![^>]*aria-current)[^>]*>/);
  });
});
