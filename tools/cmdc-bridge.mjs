#!/usr/bin/env node
// cmdc-bridge: 本地协议转换器
//   ZCode/Claude 系客户端 ──(Anthropic Messages API)──▶ 本桥 ──(cmdc 私有信封)──▶ https://api.commandcode.ai/alpha/generate
//
// 用法: node cmdc-bridge.mjs
//   PORT=8787                    监听端口
//   CMDC_KEY=user_xxx            覆盖密钥（缺省读 ~/.commandcode/auth.json 的 apiKey）
//   CMDC_UPSTREAM=https://...    上游地址
//   CMDC_MODEL=deepseek/deepseek-v4-flash   固定模型（客户端传什么都被映射到它）
//
// 已逆向的 wire 协议（2026-09-02，CLI v1.40.1）：
//   请求体 = { config, memory:null, taste:null, skills:null, permissionMode, threadId,
//              params: { model, messages, tools, system, max_tokens, stream } }
//   params.messages 内容块为 AI SDK 风格：text / reasoning / tool-call(toolCallId,toolName,input)
//   工具结果消息：{ role:"tool", content:[{ type:"tool-result", toolCallId, toolName, output:{type:"text",value} }] }
//   响应 = NDJSON SSE：start / start-step / reasoning-{start,delta,end} / text-{start,delta,end} /
//          tool-input-{start,delta,end} / tool-call / finish-step / finish / provider-metadata
// 协议属未公开 alpha 面，上游改动时本桥 fail-fast 报错，重新抓包校准即可。

import http from 'node:http';
import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

const PORT = Number(process.env.PORT || 8787);
const UPSTREAM = (process.env.CMDC_UPSTREAM || 'https://api.commandcode.ai').replace(/\/+$/, '');
const FIXED_MODEL = process.env.CMDC_MODEL || 'deepseek/deepseek-v4-flash';

function resolveKey() {
  if (process.env.CMDC_KEY) return process.env.CMDC_KEY;
  try {
    const auth = JSON.parse(readFileSync(join(homedir(), '.commandcode', 'auth.json'), 'utf8'));
    if (auth.apiKey) return auth.apiKey;
  } catch {}
  return '';
}
const DEFAULT_KEY = resolveKey();

// ---------- Anthropic → cmdc 请求翻译 ----------

function collapseText(content) {
  // tool_result.content 可能是 string 或 [{type:"text",text}]，折叠成纯文本
  if (typeof content === 'string') return content;
  if (Array.isArray(content)) {
    return content.map(b => (b?.type === 'text' ? b.text : JSON.stringify(b))).join('\n');
  }
  return String(content ?? '');
}

function translateMessages(anthropicMessages) {
  // 返回 cmdc params.messages；同时把 user 消息里的 tool_result 块改写成独立的 role:"tool" 消息
  const out = [];
  for (const msg of anthropicMessages) {
    const blocks = Array.isArray(msg.content)
      ? msg.content
      : [{ type: 'text', text: String(msg.content ?? '') }];

    if (msg.role === 'assistant') {
      const translated = [];
      for (const b of blocks) {
        if (b.type === 'text') {
          translated.push({ type: 'text', text: b.text });
        } else if (b.type === 'thinking' || b.type === 'reasoning') {
          translated.push({ type: 'reasoning', text: b.thinking ?? b.text ?? '' });
        } else if (b.type === 'tool_use') {
          translated.push({ type: 'tool-call', toolCallId: b.id, toolName: b.name, input: b.input ?? {} });
        } else if (b.type === 'image') {
          translated.push({ type: 'text', text: '[image omitted: deepseek-v4-flash is text-only]' });
        } else {
          translated.push({ type: 'text', text: JSON.stringify(b) });
        }
      }
      out.push({ role: 'assistant', content: translated });
      continue;
    }

    // user 消息：拆 text 与 tool_result
    const textBlocks = [];
    const toolResults = [];
    for (const b of blocks) {
      if (b.type === 'tool_result') {
        toolResults.push({
          type: 'tool-result',
          toolCallId: b.tool_use_id,
          toolName: b.name ?? '', // wire 样例带 toolName；客户端通常不带，回填在下方
          output: { type: 'text', value: collapseText(b.content) },
        });
      } else if (b.type === 'text') {
        textBlocks.push({ type: 'text', text: b.text });
      } else if (b.type === 'image') {
        textBlocks.push({ type: 'text', text: '[image omitted: deepseek-v4-flash is text-only]' });
      } else {
        textBlocks.push({ type: 'text', text: JSON.stringify(b) });
      }
    }
    if (textBlocks.length) out.push({ role: 'user', content: textBlocks });
    if (toolResults.length) out.push({ role: 'tool', content: toolResults });
  }

  // 回填 toolName：从历史 assistant 的 tool-call 里找 id→name
  const idToName = new Map();
  for (const m of out) {
    if (m.role !== 'assistant') continue;
    for (const b of m.content) if (b.type === 'tool-call') idToName.set(b.toolCallId, b.toolName);
  }
  for (const m of out) {
    if (m.role !== 'tool') continue;
    for (const b of m.content) if (!b.toolName) b.toolName = idToName.get(b.toolCallId) ?? 'unknown';
  }
  return out;
}

function systemToString(system) {
  if (!system) return undefined;
  if (typeof system === 'string') return system;
  if (Array.isArray(system)) return system.map(b => b?.text ?? '').join('\n');
  return String(system);
}

function buildUpstreamBody(anthropicBody) {
  return {
    config: {
      workingDir: process.cwd(),
      date: new Date().toISOString().slice(0, 10),
      environment: process.platform,
      isGitRepo: false,
      currentBranch: '',
      mainBranch: '',
      gitStatus: '',
      recentCommits: [],
      structure: [], // 服务端 zod 必需字段；空数组即可通过校验
    },
    memory: null,
    taste: null,
    skills: null,
    permissionMode: 'bypass', // 枚举: default|standard|auto-accept|plan|bypass；桥不执行工具，取最宽
    threadId: randomUUID(),
    params: {
      model: FIXED_MODEL,
      messages: translateMessages(anthropicBody.messages ?? []),
      tools: Array.isArray(anthropicBody.tools) && anthropicBody.tools.length ? anthropicBody.tools : undefined,
      system: systemToString(anthropicBody.system),
      max_tokens: Math.min(Math.max(anthropicBody.max_tokens ?? 8192, 1), 64000), // 上游 zod 硬顶 200000，但 deepseek 模型级更紧；64000 是 cmdc CLI 自身用的值，取保守上限
      stream: true, // 上游统一走 SSE，桥内再按客户端诉求聚合或转发
    },
  };
}

// ---------- cmdc SSE → Anthropic 流式翻译 ----------

const STOP_MAP = { stop: 'end_turn', 'tool-calls': 'tool_use', length: 'max_tokens', 'content-filter': 'end_turn', error: 'end_turn', other: 'end_turn' };

class AnthropicStream {
  constructor(res, model, silent = false) {
    this.res = res;
    this.model = model;
    this.silent = silent; // stream:false 时只聚合不写响应
    this.nextIndex = 0;
    this.openBlocks = new Map(); // cmdc block id → anthropic index
    this.emittedToolUse = false;
    this.usage = { input_tokens: 0, output_tokens: 0 };
    this.stopReason = 'end_turn';
    this.agg = { text: '', thinking: '', tools: [] }; // 非流式聚合用
    this.started = false;
  }

  sse(event, data) {
    if (!this.silent && !this.res.writableEnded) this.res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`);
  }
  // 非 SSE 分行 NDJSON 上游 → 统一从这里进；首个事件时先发 message_start
  ensureStarted() {
    if (this.started) return;
    this.started = true;
    this.sse('message_start', {
      type: 'message_start',
      message: { id: `msg_${randomUUID()}`, type: 'message', role: 'assistant', model: this.model, content: [], stop_reason: null, stop_sequence: null, usage: { input_tokens: 0, output_tokens: 0 } },
    });
  }

  openBlock(id, contentBlock) {
    const index = this.nextIndex++;
    this.openBlocks.set(id, index);
    this.sse('content_block_start', { type: 'content_block_start', index, content_block: contentBlock });
    return index;
  }
  closeBlock(id) {
    const index = this.openBlocks.get(id);
    if (index === undefined) return;
    this.openBlocks.delete(id);
    this.sse('content_block_end', { type: 'content_block_end', index });
  }

  handle(evt) {
    switch (evt.type) {
      case 'start':
      case 'start-step':
      case 'provider-metadata':
      case 'tool-call': // input 已通过 input_json_delta 完整流式，无需重复
        return;
      case 'reasoning-start': {
        this.ensureStarted();
        this.openBlock(evt.id, { type: 'thinking', thinking: '' });
        return;
      }
      case 'reasoning-delta': {
        this.ensureStarted();
        this.agg.thinking += evt.text ?? '';
        this.sse('content_block_delta', { type: 'content_block_delta', index: this.openBlocks.get(evt.id), delta: { type: 'thinking_delta', thinking: evt.text ?? '' } });
        return;
      }
      case 'reasoning-end':
        this.closeBlock(evt.id);
        return;
      case 'text-start': {
        this.ensureStarted();
        this.openBlock(evt.id, { type: 'text', text: '' });
        return;
      }
      case 'text-delta': {
        this.ensureStarted();
        this.agg.text += evt.text ?? '';
        this.sse('content_block_delta', { type: 'content_block_delta', index: this.openBlocks.get(evt.id), delta: { type: 'text_delta', text: evt.text ?? '' } });
        return;
      }
      case 'text-end':
        this.closeBlock(evt.id);
        return;
      case 'tool-input-start': {
        this.ensureStarted();
        this.emittedToolUse = true;
        const id = evt.id ?? `toolu_${randomUUID()}`;
        this.agg.tools.push({ id, name: evt.toolName, input: '' });
        this.openBlock(id, { type: 'tool_use', id, name: evt.toolName ?? 'unknown', input: {} });
        return;
      }
      case 'tool-input-delta': {
        const idx = this.openBlocks.get(evt.id);
        const tool = this.agg.tools.find(t => t.id === evt.id);
        if (tool) tool.input += evt.delta ?? '';
        this.sse('content_block_delta', { type: 'content_block_delta', index: idx, delta: { type: 'input_json_delta', partial_json: evt.delta ?? '' } });
        return;
      }
      case 'tool-input-end':
        this.closeBlock(evt.id);
        return;
      case 'finish-step': {
        const u = evt.usage ?? {};
        this.usage.input_tokens = u.inputTokens ?? this.usage.input_tokens;
        this.usage.output_tokens = u.outputTokens ?? this.usage.output_tokens;
        const fr = STOP_MAP[evt.finishReason] ?? 'end_turn';
        if (evt.finishReason && evt.finishReason !== 'stop') this.stopReason = fr;
        return;
      }
      case 'finish': {
        const u = evt.totalUsage ?? {};
        this.usage.input_tokens = u.inputTokens ?? this.usage.input_tokens;
        this.usage.output_tokens = u.outputTokens ?? this.usage.output_tokens;
        return;
      }
      default:
        return;
    }
  }

  // 上游流正常结束后收尾
  finalize() {
    this.ensureStarted();
    for (const id of [...this.openBlocks.keys()]) this.closeBlock(id);
    if (this.emittedToolUse) this.stopReason = 'tool_use';
    this.sse('message_delta', {
      type: 'message_delta',
      delta: { stop_reason: this.stopReason, stop_sequence: null },
      usage: this.usage,
    });
    this.sse('message_stop', { type: 'message_stop' });
    this.res.end();
  }

  abortWith(message) {
    if (this.silent) {
      if (!this.res.writableEnded && !this.res.headersSent) {
        res502(this.res, message);
      } else if (!this.res.writableEnded) {
        this.res.end();
      }
      return;
    }
    this.ensureStarted();
    this.sse('error', { type: 'error', error: { type: 'api_error', message } });
    this.res.end();
  }

  // stream:false 时聚合为完整 message
  toMessage() {
    const content = [];
    if (this.agg.thinking) content.push({ type: 'thinking', thinking: this.agg.thinking });
    if (this.agg.text) content.push({ type: 'text', text: this.agg.text });
    for (const t of this.agg.tools) {
      let input = {};
      try { input = JSON.parse(t.input || '{}'); } catch {}
      content.push({ type: 'tool_use', id: t.id, name: t.name, input });
    }
    if (this.emittedToolUse) this.stopReason = 'tool_use';
    return {
      id: `msg_${randomUUID()}`, type: 'message', role: 'assistant', model: this.model,
      content, stop_reason: this.stopReason, stop_sequence: null, usage: this.usage,
    };
  }
}

// ---------- 上游调用 ----------

async function callUpstream(cmdcBody, clientKey, signal) {
  const threadId = cmdcBody.threadId;
  const resp = await fetch(`${UPSTREAM}/alpha/generate`, {
    method: 'POST',
    signal,
    headers: {
      'content-type': 'application/json',
      'user-agent': 'cli',
      'x-command-code-version': process.env.CMDC_VERSION || '1.40.1',
      'x-cli-environment': 'production',
      authorization: `Bearer ${clientKey}`,
      'x-session-id': threadId,
    },
    body: JSON.stringify(cmdcBody),
  });
  return resp;
}

// NDJSON SSE 逐行解析：上游是每行一个 JSON 对象（无 "event:" 前缀行）
async function* upstreamLines(respBody, signal) {
  const reader = respBody.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  try {
    while (true) {
      if (signal?.aborted) break;
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let nl;
      while ((nl = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, nl).trim();
        buf = buf.slice(nl + 1);
        if (!line) continue;
        try { yield JSON.parse(line); } catch {}
      }
    }
  } finally {
    try { await reader.cancel(); } catch {}
  }
}

function anthropicError(res, status, type, message) {
  const body = JSON.stringify({ type: 'error', error: { type, message } });
  res.writeHead(status, { 'content-type': 'application/json' });
  res.end(body);
}

function res502(res, message) {
  if (res.headersSent) { res.end(); return; }
  res.writeHead(502, { 'content-type': 'application/json' });
  res.end(JSON.stringify({ type: 'error', error: { type: 'api_error', message } }));
}

// ---------- HTTP 服务 ----------

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  console.error(`[access] ${req.method} ${url.pathname}`);

  if (req.method === 'GET' && url.pathname === '/health') {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ ok: true, upstream: UPSTREAM, model: FIXED_MODEL }));
    return;
  }

  if (req.method === 'GET' && url.pathname === '/v1/models') {
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ object: 'list', data: [{ id: FIXED_MODEL, object: 'model', owned_by: 'cmdc' }] }));
    return;
  }

  if (req.method === 'POST' && url.pathname === '/v1/messages') {
    const chunks = [];
    for await (const c of req) chunks.push(c);
    let anthropicBody;
    try { anthropicBody = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch {
      anthropicError(res, 400, 'invalid_request_error', 'body is not valid JSON');
      return;
    }

    // 密钥：客户端带来的优先（x-api-key 或 Authorization），否则用本机 auth.json
    const clientKey = req.headers['x-api-key'] || (req.headers.authorization || '').replace(/^Bearer\s+/i, '') || DEFAULT_KEY;
    if (!clientKey) {
      anthropicError(res, 401, 'authentication_error', 'no cmdc key: set CMDC_KEY or login via cmdc');
      return;
    }
    console.error(`[auth] key=${clientKey.slice(0, 8)}… source=${req.headers['x-api-key'] ? 'client-x-api-key' : req.headers.authorization ? 'client-authorization' : 'auth.json'}`);

    const wantStream = anthropicBody.stream !== false;
    const abort = new AbortController();
    req.on('close', () => abort.abort());

    let upstream;
    const cmdcBody = buildUpstreamBody(anthropicBody);
    try {
      upstream = await callUpstream(cmdcBody, clientKey, abort.signal);
    } catch (e) {
      anthropicError(res, 502, 'api_error', `upstream unreachable: ${e.message}`);
      return;
    }
    if (!upstream.ok) {
      const detail = (await upstream.text().catch(() => '')).slice(0, 500);
      console.error(`[upstream-error] status=${upstream.status} body=${detail}`);
      anthropicError(res, upstream.status === 401 || upstream.status === 403 ? 401 : 502, upstream.status === 401 || upstream.status === 403 ? 'authentication_error' : 'api_error', `upstream ${upstream.status}: ${detail}`);
      return;
    }
    if (!upstream.body) {
      anthropicError(res, 502, 'api_error', 'upstream returned empty body');
      return;
    }

    const stream = new AnthropicStream(res, FIXED_MODEL, !wantStream);
    try {
      for await (const evt of upstreamLines(upstream.body, abort.signal)) {
        if (evt && typeof evt.type === 'string') stream.handle(evt);
      }
      if (wantStream) {
        stream.finalize();
      } else {
        res.writeHead(200, { 'content-type': 'application/json' });
        res.end(JSON.stringify(stream.toMessage()));
      }
    } catch (e) {
      stream.abortWith(`upstream stream error: ${e.message}`);
    }

    console.error(`[bridge] ${wantStream ? 'stream' : 'block'} in=${stream.usage.input_tokens} out=${stream.usage.output_tokens} stop=${stream.stopReason} tools=${stream.agg.tools.length}`);
    return;
  }

  anthropicError(res, 404, 'not_found_error', `no route: ${req.method} ${url.pathname}`);
});

server.listen(PORT, '127.0.0.1', () => {
  console.error(`[bridge] listening http://127.0.0.1:${PORT} → ${UPSTREAM}/alpha/generate (model: ${FIXED_MODEL})`);
  console.error(`[bridge] key source: ${process.env.CMDC_KEY ? 'CMDC_KEY' : DEFAULT_KEY ? '~/.commandcode/auth.json' : 'NONE (will 401)'}`);
});
