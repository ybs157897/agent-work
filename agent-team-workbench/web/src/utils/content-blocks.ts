export const CONTENT_BLOCK_VERSION = 'languagegui/v1' as const;

const MAX_BLOCKS = 12;
const MAX_METRICS = 8;
const MAX_COLUMNS = 12;
const MAX_ROWS = 100;
const MAX_LABELS = 64;
const MAX_SERIES = 4;
const MAX_FILES = 20;
const MAX_MEDIA_ITEMS = 8;
const MAX_SEARCH_RESULTS = 12;
const MAX_REVIEW_FINDINGS = 30;
const MAX_REVIEW_CHECKS = 20;
const MAX_REVIEW_NEXT_STEPS = 12;
const MAX_CANVAS_NODES = 24;
const MAX_CANVAS_EDGES = 32;

type Scalar = string | number | boolean | null;

export type CanvasNodeKind = 'start' | 'end' | 'process' | 'decision' | 'actor' | 'system' | 'note';

export interface ContentSource {
  label: string;
  url?: string;
}

interface ContentBlockBase {
  id?: string;
  title?: string;
  description?: string;
  source?: ContentSource;
}

export type MetricTone = 'neutral' | 'positive' | 'warning' | 'negative';

export interface MetricBlock extends ContentBlockBase {
  type: 'metric';
  items: Array<{
    label: string;
    value: string;
    detail?: string;
    delta?: string;
    tone: MetricTone;
  }>;
}

export interface TableBlock extends ContentBlockBase {
  type: 'table';
  columns: Array<{ key: string; label: string; align: 'left' | 'center' | 'right' }>;
  rows: Array<Record<string, Scalar>>;
}

export interface ChartBlock extends ContentBlockBase {
  type: 'chart';
  chart: 'bar' | 'line';
  labels: string[];
  series: Array<{ name: string; values: number[] }>;
  unit?: string;
  yDomain?: 'zero' | 'auto';
}

export interface FileBlock extends ContentBlockBase {
  type: 'file';
  files: Array<{
    name: string;
    size?: string;
    mime?: string;
    status?: 'ready' | 'draft' | 'processing' | 'failed' | 'accepted';
    url?: string;
  }>;
}

export interface EventBlock extends ContentBlockBase {
  type: 'event';
  title: string;
  start: string;
  end?: string;
  location?: string;
  timezone?: string;
  url?: string;
}

export interface ImageBlock extends ContentBlockBase {
  type: 'image';
  images: Array<{ src: string; alt: string; caption?: string }>;
}

export interface AudioBlock extends ContentBlockBase {
  type: 'audio';
  tracks: Array<{ src: string; title: string; duration?: string }>;
}

export interface MapBlock extends ContentBlockBase {
  type: 'map';
  location: string;
  latitude?: number;
  longitude?: number;
  imageUrl?: string;
  url?: string;
}

export interface SearchBlock extends ContentBlockBase {
  type: 'search';
  query?: string;
  results: Array<{ title: string; url: string; snippet?: string; source?: string }>;
}

export interface RatingBlock extends ContentBlockBase {
  type: 'rating';
  question: string;
  lowLabel?: string;
  highLabel?: string;
}

export type ReviewVerdict = 'passed' | 'passed_with_warnings' | 'changes_requested' | 'blocked' | 'inconclusive';
export type ReviewSeverity = 'critical' | 'high' | 'medium' | 'low' | 'info';

export interface ReviewSummaryBlock extends ContentBlockBase {
  type: 'review-summary';
  verdict: ReviewVerdict;
  summary: string;
  stats?: { files?: number; findings?: number; passed?: number };
  findings: Array<{
    severity: ReviewSeverity;
    title: string;
    detail?: string;
    file?: string;
    line?: number;
    evidence?: string;
    suggestion?: string;
    url?: string;
  }>;
  checks: Array<{
    label: string;
    status: 'passed' | 'failed' | 'warning' | 'skipped' | 'running';
    detail?: string;
    command?: string;
  }>;
  nextSteps: Array<{ label: string; detail?: string }>;
}

export interface CanvasNode {
  id: string;
  label: string;
  detail?: string;
  kind: CanvasNodeKind;
  x?: number;
  y?: number;
}

export interface CanvasEdge {
  from: string;
  to: string;
  label?: string;
}

export interface CanvasBlock extends ContentBlockBase {
  type: 'canvas';
  nodes: CanvasNode[];
  edges: CanvasEdge[];
}

export type ContentBlock = MetricBlock | TableBlock | ChartBlock | FileBlock | EventBlock | ImageBlock | AudioBlock | MapBlock | SearchBlock | RatingBlock | ReviewSummaryBlock | CanvasBlock;

export interface ContentBlockDocument {
  version: typeof CONTENT_BLOCK_VERSION;
  blocks: ContentBlock[];
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : null;
}

function text(value: unknown, max: number): string | undefined {
  if (typeof value !== 'string') return undefined;
  const normalized = value.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '').trim();
  return normalized ? normalized.slice(0, max) : undefined;
}

function scalar(value: unknown): Scalar | undefined {
  if (value === null || typeof value === 'string' || typeof value === 'boolean') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  return undefined;
}

export function isSafeContentUrl(value: string | undefined): boolean {
  const url = value?.trim() ?? '';
  if (!url || url.length > 2_048 || /[\u0000-\u001F\u007F]/.test(url)) return false;
  if (/^https?:\/\//i.test(url)) return true;
  if (/^\/(?!\/)/.test(url) || /^\.\.?\//.test(url) || /^#/.test(url)) return true;
  return false;
}

function safeUrl(value: unknown): string | undefined {
  const candidate = text(value, 2_048);
  return isSafeContentUrl(candidate) ? candidate : undefined;
}

function source(value: unknown): ContentSource | undefined {
  const item = record(value);
  if (!item) return undefined;
  const label = text(item.label, 120);
  if (!label) return undefined;
  const url = safeUrl(item.url);
  return { label, ...(url ? { url } : {}) };
}

function base(value: Record<string, unknown>): ContentBlockBase {
  const id = text(value.id, 80);
  const title = text(value.title, 160);
  const description = text(value.description, 500);
  const parsedSource = source(value.source);
  return {
    ...(id ? { id } : {}),
    ...(title ? { title } : {}),
    ...(description ? { description } : {}),
    ...(parsedSource ? { source: parsedSource } : {}),
  };
}

function parseMetric(value: Record<string, unknown>): MetricBlock | null {
  if (!Array.isArray(value.items)) return null;
  const items: MetricBlock['items'] = [];
  for (const raw of value.items.slice(0, MAX_METRICS)) {
    const item = record(raw);
    if (!item) continue;
    const label = text(item.label, 120);
    const rawValue = scalar(item.value);
    if (!label || rawValue === undefined || rawValue === null) continue;
    const tone: MetricTone = item.tone === 'positive' || item.tone === 'warning' || item.tone === 'negative'
      ? item.tone
      : 'neutral';
    const detail = text(item.detail, 240);
    const delta = text(item.delta, 120);
    items.push({
      label,
      value: String(rawValue).slice(0, 160),
      tone,
      ...(detail ? { detail } : {}),
      ...(delta ? { delta } : {}),
    });
  }
  return items.length ? { type: 'metric', ...base(value), items } : null;
}

function parseTable(value: Record<string, unknown>): TableBlock | null {
  if (!Array.isArray(value.columns) || !Array.isArray(value.rows)) return null;
  const columns: TableBlock['columns'] = [];
  const seen = new Set<string>();
  for (const raw of value.columns.slice(0, MAX_COLUMNS)) {
    const column = record(raw);
    if (!column) continue;
    const key = text(column.key, 80);
    const label = text(column.label, 120);
    if (!key || !label || seen.has(key)) continue;
    seen.add(key);
    const align = column.align === 'center' || column.align === 'right' ? column.align : 'left';
    columns.push({ key, label, align });
  }
  if (!columns.length) return null;
  const rows: TableBlock['rows'] = [];
  for (const raw of value.rows.slice(0, MAX_ROWS)) {
    const row = record(raw);
    if (!row) continue;
    const normalized: Record<string, Scalar> = {};
    for (const column of columns) {
      const cell = scalar(row[column.key]);
      normalized[column.key] = cell === undefined
        ? null
        : typeof cell === 'string'
          ? cell.slice(0, 500)
          : cell;
    }
    rows.push(normalized);
  }
  return { type: 'table', ...base(value), columns, rows };
}

function parseChart(value: Record<string, unknown>): ChartBlock | null {
  if ((value.chart !== 'bar' && value.chart !== 'line') || !Array.isArray(value.labels) || !Array.isArray(value.series)) {
    return null;
  }
  const allLabels = value.labels.map((label) => scalar(label));
  if (!allLabels.length || allLabels.some((label) => label === undefined || label === null)) return null;
  const labels = allLabels.slice(0, MAX_LABELS).map((label) => String(label).slice(0, 80));
  const series: ChartBlock['series'] = [];
  for (const raw of value.series.slice(0, MAX_SERIES)) {
    const item = record(raw);
    if (!item || !Array.isArray(item.values) || item.values.length !== value.labels.length) continue;
    const values = item.values.slice(0, labels.length);
    const name = text(item.name, 120);
    if (!name || values.some((point) => typeof point !== 'number' || !Number.isFinite(point))) continue;
    series.push({ name, values: values as number[] });
  }
  if (!series.length) return null;
  const unit = text(value.unit, 40);
  const yDomainValue = value.y_domain ?? value.yDomain;
  const yDomain = yDomainValue === 'auto' ? 'auto' : yDomainValue === 'zero' ? 'zero' : undefined;
  return {
    type: 'chart',
    ...base(value),
    chart: value.chart,
    labels,
    series,
    ...(unit ? { unit } : {}),
    ...(yDomain ? { yDomain } : {}),
  };
}

function parseFile(value: Record<string, unknown>): FileBlock | null {
  if (!Array.isArray(value.files)) return null;
  const statuses = new Set(['ready', 'draft', 'processing', 'failed', 'accepted']);
  const files: FileBlock['files'] = [];
  for (const raw of value.files.slice(0, MAX_FILES)) {
    const item = record(raw);
    if (!item) continue;
    const name = text(item.name, 240);
    if (!name) continue;
    const sizeValue = scalar(item.size);
    const size = sizeValue === undefined || sizeValue === null ? undefined : String(sizeValue).slice(0, 80);
    const mime = text(item.mime, 120);
    const status = typeof item.status === 'string' && statuses.has(item.status)
      ? item.status as NonNullable<FileBlock['files'][number]['status']>
      : undefined;
    const url = safeUrl(item.url);
    files.push({
      name,
      ...(size ? { size } : {}),
      ...(mime ? { mime } : {}),
      ...(status ? { status } : {}),
      ...(url ? { url } : {}),
    });
  }
  return files.length ? { type: 'file', ...base(value), files } : null;
}

function parseEvent(value: Record<string, unknown>): EventBlock | null {
  const title = text(value.title, 160);
  const start = text(value.start, 80);
  if (!title || !start) return null;
  const end = text(value.end, 80);
  const location = text(value.location, 240);
  const timezone = text(value.timezone, 80);
  const url = safeUrl(value.url);
  return {
    type: 'event',
    ...base(value),
    title,
    start,
    ...(end ? { end } : {}),
    ...(location ? { location } : {}),
    ...(timezone ? { timezone } : {}),
    ...(url ? { url } : {}),
  };
}

function parseImage(value: Record<string, unknown>): ImageBlock | null {
  if (!Array.isArray(value.images)) return null;
  const images: ImageBlock['images'] = [];
  for (const raw of value.images.slice(0, MAX_MEDIA_ITEMS)) {
    const item = record(raw);
    if (!item) continue;
    const src = safeUrl(item.src);
    const alt = text(item.alt, 240);
    const caption = text(item.caption, 300);
    if (!src || !alt) continue;
    images.push({ src, alt, ...(caption ? { caption } : {}) });
  }
  return images.length ? { type: 'image', ...base(value), images } : null;
}

function parseAudio(value: Record<string, unknown>): AudioBlock | null {
  if (!Array.isArray(value.tracks)) return null;
  const tracks: AudioBlock['tracks'] = [];
  for (const raw of value.tracks.slice(0, MAX_MEDIA_ITEMS)) {
    const item = record(raw);
    if (!item) continue;
    const src = safeUrl(item.src);
    const title = text(item.title, 180);
    const duration = text(item.duration, 40);
    if (!src || !title) continue;
    tracks.push({ src, title, ...(duration ? { duration } : {}) });
  }
  return tracks.length ? { type: 'audio', ...base(value), tracks } : null;
}

function coordinate(value: unknown, min: number, max: number): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= min && value <= max ? value : undefined;
}

function parseMap(value: Record<string, unknown>): MapBlock | null {
  const location = text(value.location, 240);
  if (!location) return null;
  const latitude = coordinate(value.latitude, -90, 90);
  const longitude = coordinate(value.longitude, -180, 180);
  const imageUrl = safeUrl(value.image_url);
  const url = safeUrl(value.url);
  return {
    type: 'map',
    ...base(value),
    location,
    ...(latitude !== undefined ? { latitude } : {}),
    ...(longitude !== undefined ? { longitude } : {}),
    ...(imageUrl ? { imageUrl } : {}),
    ...(url ? { url } : {}),
  };
}

function parseSearch(value: Record<string, unknown>): SearchBlock | null {
  if (!Array.isArray(value.results)) return null;
  const results: SearchBlock['results'] = [];
  for (const raw of value.results.slice(0, MAX_SEARCH_RESULTS)) {
    const item = record(raw);
    if (!item) continue;
    const title = text(item.title, 180);
    const url = safeUrl(item.url);
    const snippet = text(item.snippet, 500);
    const resultSource = text(item.source, 120);
    if (!title || !url) continue;
    results.push({ title, url, ...(snippet ? { snippet } : {}), ...(resultSource ? { source: resultSource } : {}) });
  }
  if (!results.length) return null;
  const query = text(value.query, 240);
  return { type: 'search', ...base(value), ...(query ? { query } : {}), results };
}

function parseRating(value: Record<string, unknown>): RatingBlock | null {
  const question = text(value.question, 240);
  if (!question) return null;
  const lowLabel = text(value.low_label, 80);
  const highLabel = text(value.high_label, 80);
  return { type: 'rating', ...base(value), question, ...(lowLabel ? { lowLabel } : {}), ...(highLabel ? { highLabel } : {}) };
}

function enumValue<T extends string>(value: unknown, values: readonly T[]): T | undefined {
  return typeof value === 'string' && values.includes(value as T) ? value as T : undefined;
}

function reviewCount(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0 && value <= 1_000_000
    ? value
    : undefined;
}

function parseReviewSummary(value: Record<string, unknown>): ReviewSummaryBlock | null {
  const verdict = enumValue(value.verdict, ['passed', 'passed_with_warnings', 'changes_requested', 'blocked', 'inconclusive'] as const);
  const summary = text(value.summary, 1_000);
  if (!verdict || !summary) return null;

  const statsValue = record(value.stats);
  const filesCount = reviewCount(statsValue?.files);
  const findingsCount = reviewCount(statsValue?.findings);
  const passedCount = reviewCount(statsValue?.passed);
  const stats = statsValue ? {
    ...(filesCount !== undefined ? { files: filesCount } : {}),
    ...(findingsCount !== undefined ? { findings: findingsCount } : {}),
    ...(passedCount !== undefined ? { passed: passedCount } : {}),
  } : undefined;

  const findings: ReviewSummaryBlock['findings'] = [];
  for (const raw of (Array.isArray(value.findings) ? value.findings : []).slice(0, MAX_REVIEW_FINDINGS)) {
    const item = record(raw);
    if (!item) continue;
    const severity = enumValue(item.severity, ['critical', 'high', 'medium', 'low', 'info'] as const);
    const title = text(item.title, 240);
    const detail = text(item.detail, 1_000);
    if (!severity || !title) continue;
    const file = text(item.file, 240);
    const line = typeof item.line === 'number' && Number.isInteger(item.line) && item.line > 0 && item.line <= 10_000_000 ? item.line : undefined;
    const evidence = text(item.evidence, 2_000);
    const suggestion = text(item.suggestion, 1_000);
    const url = safeUrl(item.url);
    findings.push({ severity, title, ...(detail ? { detail } : {}), ...(file ? { file } : {}), ...(line !== undefined ? { line } : {}), ...(evidence ? { evidence } : {}), ...(suggestion ? { suggestion } : {}), ...(url ? { url } : {}) });
  }

  const checks: ReviewSummaryBlock['checks'] = [];
  for (const raw of (Array.isArray(value.checks) ? value.checks : []).slice(0, MAX_REVIEW_CHECKS)) {
    const item = record(raw);
    if (!item) continue;
    const label = text(item.label, 240);
    const status = enumValue(item.status, ['passed', 'failed', 'warning', 'skipped', 'running'] as const);
    if (!label || !status) continue;
    const detail = text(item.detail, 500);
    const command = text(item.command, 500);
    checks.push({ label, status, ...(detail ? { detail } : {}), ...(command ? { command } : {}) });
  }

  const nextSteps: ReviewSummaryBlock['nextSteps'] = [];
  const rawNextSteps = Array.isArray(value.next_steps) ? value.next_steps : Array.isArray(value.nextSteps) ? value.nextSteps : [];
  for (const raw of rawNextSteps.slice(0, MAX_REVIEW_NEXT_STEPS)) {
    const item = record(raw);
    if (!item) continue;
    const label = text(item.label, 240);
    if (!label) continue;
    const detail = text(item.detail, 500);
    nextSteps.push({ label, ...(detail ? { detail } : {}) });
  }
  if (!findings.length && !checks.length && !nextSteps.length) return null;
  const normalizedStats = stats && Object.keys(stats).length ? {
    ...stats,
    ...(stats.findings !== undefined ? { findings: findings.length } : {}),
    ...(stats.passed !== undefined ? { passed: checks.filter((check) => check.status === 'passed').length } : {}),
  } : undefined;
  return { type: 'review-summary', ...base(value), verdict, summary, ...(normalizedStats ? { stats: normalizedStats } : {}), findings, checks, nextSteps };
}

function canvasCoordinate(value: unknown, max: number): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= max ? value : undefined;
}

function parseCanvas(value: Record<string, unknown>): CanvasBlock | null {
  if (!Array.isArray(value.nodes)) return null;
  const nodeKinds = new Set<CanvasNodeKind>(['start', 'end', 'process', 'decision', 'actor', 'system', 'note']);
  const nodes: CanvasNode[] = [];
  const seenIds = new Set<string>();
  for (const raw of value.nodes.slice(0, MAX_CANVAS_NODES)) {
    const item = record(raw);
    if (!item) continue;
    const id = text(item.id, 40);
    const label = text(item.label, 120);
    if (!id || !label || seenIds.has(id)) continue;
    seenIds.add(id);
    const kind = enumValue(item.kind, [...nodeKinds] as CanvasNodeKind[]) ?? 'process';
    const detail = text(item.detail, 240);
    const x = canvasCoordinate(item.x, 1_000);
    const y = canvasCoordinate(item.y, 1_000);
    nodes.push({
      id,
      label,
      kind,
      ...(detail ? { detail } : {}),
      ...(x !== undefined ? { x } : {}),
      ...(y !== undefined ? { y } : {}),
    });
  }
  if (!nodes.length) return null;

  const edges: CanvasEdge[] = [];
  for (const raw of (Array.isArray(value.edges) ? value.edges : []).slice(0, MAX_CANVAS_EDGES)) {
    const item = record(raw);
    if (!item) continue;
    const from = text(item.from, 40);
    const to = text(item.to, 40);
    if (!from || !to || !seenIds.has(from) || !seenIds.has(to) || from === to) continue;
    const label = text(item.label, 120);
    edges.push({ from, to, ...(label ? { label } : {}) });
  }

  return { type: 'canvas', ...base(value), nodes, edges };
}

function parseBlock(value: unknown): ContentBlock | null {
  const item = record(value);
  if (!item || typeof item.type !== 'string') return null;
  switch (item.type) {
    case 'metric':
      return parseMetric(item);
    case 'table':
      return parseTable(item);
    case 'chart':
      return parseChart(item);
    case 'file':
      return parseFile(item);
    case 'event':
      return parseEvent(item);
    case 'image':
      return parseImage(item);
    case 'audio':
      return parseAudio(item);
    case 'map':
      return parseMap(item);
    case 'search':
      return parseSearch(item);
    case 'rating':
      return parseRating(item);
    case 'review-summary':
      return parseReviewSummary(item);
    case 'canvas':
      return parseCanvas(item);
    default:
      return null;
  }
}

export function parseContentBlockDocument(input: unknown): ContentBlockDocument | null {
  let value = input;
  if (typeof input === 'string') {
    if (input.length > 250_000) return null;
    try {
      value = JSON.parse(input) as unknown;
    } catch {
      return null;
    }
  }
  const envelope = record(value);
  if (!envelope || envelope.version !== CONTENT_BLOCK_VERSION || !Array.isArray(envelope.blocks)) return null;
  const blocks = envelope.blocks.slice(0, MAX_BLOCKS).map(parseBlock).filter((block): block is ContentBlock => block !== null);
  return blocks.length ? { version: CONTENT_BLOCK_VERSION, blocks } : null;
}

/** canonical content_blocks 存在时移除同源 fenced JSON，避免双份 Widget。 */
export function stripLanguageGuiFences(markdown: string): string {
  return markdown
    .replace(/```(?:languagegui|lgui)[^\n]*\n[\s\S]*?```/gi, '')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
}
