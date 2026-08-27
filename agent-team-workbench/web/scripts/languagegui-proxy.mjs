// LanguageGUI 演示代理（支线 zcode/languagegui-demo，零依赖，仅本机 127.0.0.1）。
// 职责：演示页 → 本代理 → registry.yaml 声明的 openai-completions 供应商流式转发。
// 凭据读取 models/credentials.local.yaml（gitignored；worktree 无此文件时回退主
// worktree），密钥只进请求头、永不打印。用法：node scripts/languagegui-proxy.mjs
// 环境变量：LGUI_PORT（默认 8790）、LGUI_MODELS_DIR（默认自动定位）。

import { execSync } from 'node:child_process';
import { createServer } from 'node:http';
import { existsSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const PORT = Number(process.env.LGUI_PORT || 8790);

function findModelsDir() {
  if (process.env.LGUI_MODELS_DIR) return process.env.LGUI_MODELS_DIR;
  const local = join(WEB_DIR, '..', 'models');
  if (existsSync(join(local, 'credentials.local.yaml'))) return local;
  // worktree 检出没有 gitignored 凭据：沿 git common dir 找回主 worktree。
  const common = execSync('git rev-parse --path-format=absolute --git-common-dir', { cwd: WEB_DIR })
    .toString()
    .trim();
  return join(dirname(common), 'agent-team-workbench', 'models');
}

/** registry.yaml 由 Go 定宽写出（4 空格层级），按行提取所需字段。 */
function parseRegistry(text) {
  const providers = [];
  let provider = null;
  let model = null;
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\s+$/, '');
    let m;
    if ((m = line.match(/^ {4}- id: (\S+)$/))) {
      provider = { id: m[1], models: [] };
      providers.push(provider);
      model = null;
    } else if (provider && (m = line.match(/^ {6}(label|provider|api|base_url|api_key_env): (.*)$/))) {
      provider[m[1]] = m[2].trim();
      model = null;
    } else if (provider && (m = line.match(/^ {8}- id: (\S+)$/))) {
      model = { id: m[1] };
      provider.models.push(model);
    } else if (model && (m = line.match(/^ {10}(display_name|model|context_window): (.*)$/))) {
      model[m[1]] = m[2].trim();
    }
  }
  return providers;
}

/** credentials.local.yaml：items[].provider_id + api_key。 */
function parseCredentials(text) {
  const byProvider = new Map();
  let current = null;
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    const id = line.match(/^- provider_id: (\S+)$/);
    if (id) {
      current = id[1];
      continue;
    }
    const key = line.match(/^api_key: (.+)$/);
    if (current && key) byProvider.set(current, key[1].trim());
  }
  return byProvider;
}

function loadCatalog() {
  const dir = findModelsDir();
  const providers = parseRegistry(readFileSync(join(dir, 'registry.yaml'), 'utf8'));
  // 与控制面 CredentialsStore 同序：主路径 .agent-work/，旧 models/ 仅兜底
  // （两处的 deepseek key 可能不同步，models/ 那份是 legacy 副本）。
  const candidates = [join(dir, '..', '.agent-work', 'credentials.local.yaml'), join(dir, 'credentials.local.yaml')];
  let creds = new Map();
  for (const p of candidates) {
    if (existsSync(p)) {
      creds = parseCredentials(readFileSync(p, 'utf8'));
      break;
    }
  }
  const catalog = new Map();
  for (const p of providers) {
    if (p.api !== 'openai-completions') continue;
    const key = creds.get(p.id);
    for (const m of p.models) {
      catalog.set(m.id, {
        ref: m.id,
        model: m.model || m.id,
        display: m.display_name || m.id,
        providerLabel: p.label || p.id,
        baseUrl: (p.base_url || '').replace(/\/$/, ''),
        key: key || process.env[p.api_key_env || ''] || '',
      });
    }
  }
  return { dir, catalog };
}

const { dir: modelsDir, catalog } = loadCatalog();

function readBody(req) {
  return new Promise((res, rej) => {
    let buf = '';
    req.on('data', (c) => {
      buf += c;
      if (buf.length > 1_000_000) rej(new Error('body too large'));
    });
    req.on('end', () => res(buf));
    req.on('error', rej);
  });
}

function json(res, code, obj) {
  res.writeHead(code, { 'content-type': 'application/json; charset=utf-8' });
  res.end(JSON.stringify(obj));
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (req.method === 'GET' && url.pathname === '/models') {
    return json(res, 200, {
      models: [...catalog.values()].map((m) => ({ ref: m.ref, display: m.display, provider: m.providerLabel })),
    });
  }
  if (req.method === 'POST' && url.pathname === '/chat') {
    let body;
    try {
      body = JSON.parse(await readBody(req));
    } catch {
      return json(res, 400, { error: 'invalid json body' });
    }
    const entry = catalog.get(body.model);
    if (!entry) return json(res, 400, { error: `unknown model: ${body.model}` });
    if (!entry.key) return json(res, 500, { error: `缺少 ${entry.providerLabel} 的 api_key（credentials.local.yaml）` });
    if (!Array.isArray(body.messages)) return json(res, 400, { error: 'messages 必须是数组' });
    try {
      const upstream = await fetch(`${entry.baseUrl}/chat/completions`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', authorization: `Bearer ${entry.key}`, 'accept-encoding': 'identity' },
        // temperature 不传：kimi-k2.7-code 等推理模型只接受默认值，传了反而 400。
        body: JSON.stringify({ model: entry.model, messages: body.messages, stream: true }),
        signal: AbortSignal.timeout(180_000),
      });
      if (!upstream.ok || !upstream.body) {
        const text = await upstream.text().catch(() => '');
        return json(res, 502, { error: `upstream ${upstream.status}: ${text.slice(0, 300)}` });
      }
      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache',
        connection: 'keep-alive',
        'x-accel-buffering': 'no',
      });
      const reader = upstream.body.getReader();
      const decoder = new TextDecoder();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        res.write(decoder.decode(value, { stream: true }));
      }
      return res.end();
    } catch (err) {
      if (!res.headersSent) return json(res, 502, { error: String(err && err.message ? err.message : err) });
      return res.end();
    }
  }
  return json(res, 404, { error: 'not found' });
});

server.listen(PORT, '127.0.0.1', () => {
  console.log(`[languagegui-proxy] 127.0.0.1:${PORT} · 模型目录 ${modelsDir} · ${catalog.size} 个可用模型`);
});
