/**
 * Canvas 调研 Demo — 纯静态页，不需要后端或模型。
 * 路由：/canvas-demo
 *
 * 用途：调研 LanguageGUI `canvas` 块长什么样、agent 该输出什么 JSON。
 * 与 Atlas 正式接入无关，可随时删除。
 */

import { Workflow } from 'lucide-react';
import { ContentBlockList } from '../components/chat/content-blocks/content-block-renderer';
import type { ContentBlockDocument } from '../utils/content-blocks';
import { CONTENT_BLOCK_VERSION } from '../utils/content-blocks';

const USER_JOURNEY: ContentBlockDocument = {
  version: CONTENT_BLOCK_VERSION,
  blocks: [{
    type: 'canvas',
    title: '新用户注册 · 用户旅程',
    description: 'Atlas 典型用法：把流程拆成节点，用边表达顺序与分支',
    nodes: [
      { id: 'landing', label: '打开落地页', kind: 'start' },
      { id: 'signup', label: '填写邮箱', kind: 'process', detail: '支持 Google SSO' },
      { id: 'verify', label: '验证邮箱', kind: 'process' },
      { id: 'profile', label: '完善资料', kind: 'process' },
      { id: 'home', label: '进入首页', kind: 'end' },
    ],
    edges: [
      { from: 'landing', to: 'signup' },
      { from: 'signup', to: 'verify', label: '提交' },
      { from: 'verify', to: 'profile', label: '验证通过' },
      { from: 'profile', to: 'home' },
    ],
    source: { label: '演示数据' },
  }],
};

const ARCHITECTURE: ContentBlockDocument = {
  version: CONTENT_BLOCK_VERSION,
  blocks: [{
    type: 'canvas',
    title: 'Agent Workbench · 系统上下文',
    description: '架构调研：角色（actor）、系统（system）、决策（decision）节点',
    nodes: [
      { id: 'user', label: '用户', kind: 'actor' },
      { id: 'web', label: 'Web 前端', kind: 'system' },
      { id: 'cp', label: 'Control Plane', kind: 'system' },
      { id: 'runner', label: 'Runner / Adapter', kind: 'system' },
      { id: 'agent', label: 'CLI Agent', kind: 'actor' },
      { id: 'gate', label: '需要审批？', kind: 'decision' },
      { id: 'done', label: 'Run 终态', kind: 'end' },
    ],
    edges: [
      { from: 'user', to: 'web', label: 'Chat / 看板' },
      { from: 'web', to: 'cp' },
      { from: 'cp', to: 'runner' },
      { from: 'runner', to: 'agent' },
      { from: 'agent', to: 'gate' },
      { from: 'gate', to: 'done', label: '通过' },
    ],
  }],
};

const ROADMAP: ContentBlockDocument = {
  version: CONTENT_BLOCK_VERSION,
  blocks: [{
    type: 'canvas',
    title: 'NOW / NEXT / LATER 路线图',
    nodes: [
      { id: 'now1', label: 'Canvas 调研 Demo', kind: 'process', detail: 'NOW' },
      { id: 'now2', label: 'Atlas 输出契约', kind: 'process', detail: 'NOW' },
      { id: 'next1', label: 'Artifact 持久化', kind: 'process', detail: 'NEXT' },
      { id: 'next2', label: '可编辑画布', kind: 'process', detail: 'NEXT' },
      { id: 'later', label: '多人协同', kind: 'note', detail: 'LATER' },
    ],
    edges: [
      { from: 'now1', to: 'now2' },
      { from: 'now2', to: 'next1' },
      { from: 'next1', to: 'next2' },
      { from: 'next2', to: 'later' },
    ],
  }],
};

const EXAMPLES = [
  { id: 'journey', label: '用户旅程', doc: USER_JOURNEY },
  { id: 'arch', label: '系统架构', doc: ARCHITECTURE },
  { id: 'roadmap', label: '路线图', doc: ROADMAP },
] as const;

const SAMPLE_JSON = String.raw`{"version":"languagegui/v1","blocks":[{"type":"canvas","title":"需求评审流程","nodes":[{"id":"start","label":"提出需求","kind":"start"},{"id":"review","label":"产品评审","kind":"process"},{"id":"done","label":"进入开发","kind":"end"}],"edges":[{"from":"start","to":"review"},{"from":"review","to":"done","label":"通过"}]}]}`;

export default function CanvasDemoPage() {
  return (
    <div className="canvas-demo">
      <header className="canvas-demo-header">
        <div className="canvas-demo-brand">
          <Workflow className="h-5 w-5" aria-hidden />
          <span>Canvas 调研 Demo</span>
        </div>
        <p className="canvas-demo-lead">
          这是<strong>只读调研页</strong>，展示 agent 通过 <code>languagegui</code> JSON 输出画布时长什么样。
          不需要启动后端；与 PR #2 同一分支上的渲染能力。
        </p>
        <a className="canvas-demo-back" href="/">← 返回工作台</a>
      </header>

      <section className="canvas-demo-section">
        <h2>Agent 怎么输出？</h2>
        <p>
          Chat 运行时会把 <code>output_contract=languagegui/v1</code> 追加到 system prompt。
          当 Atlas（或其他 agent）判断「图比文字清楚」时，在回复里加一个 fenced block：
        </p>
        <pre className="canvas-demo-code">{`\`\`\`languagegui\n${SAMPLE_JSON}\n\`\`\``}</pre>
        <ul className="canvas-demo-notes">
          <li><strong>节点 kind</strong>：start / end / process / decision / actor / system / note</li>
          <li><strong>上限</strong>：24 节点、32 条边；id 唯一</li>
          <li><strong>布局</strong>：默认自动排版；可选 x/y（0–1000）手动定位</li>
          <li><strong>现状</strong>：只读展示，暂不支持拖拽编辑</li>
        </ul>
      </section>

      {EXAMPLES.map(({ id, label, doc }) => (
        <section key={id} className="canvas-demo-section">
          <h2>{label}</h2>
          <div className="canvas-demo-preview">
            <ContentBlockList document={doc} />
          </div>
        </section>
      ))}

      <section className="canvas-demo-section canvas-demo-section-muted">
        <h2>和外部画布工具的区别</h2>
        <table className="canvas-demo-table">
          <thead>
            <tr><th>方案</th><th>控制方式</th><th>特点</th></tr>
          </thead>
          <tbody>
            <tr><td>本仓库 canvas 块</td><td>agent 输出 JSON</td><td>嵌在 Chat 里，零外部依赖，只读</td></tr>
            <tr><td>OpenBoard / mcp_excalidraw</td><td>MCP / CLI</td><td>独立白板，agent 可读写形状</td></tr>
            <tr><td>Orca</td><td>orca CLI</td><td>Kanban 看板，不是无限画布</td></tr>
          </tbody>
        </table>
      </section>
    </div>
  );
}
