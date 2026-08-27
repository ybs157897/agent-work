export const MAX_PROMPT_FILES = 4;
export const MAX_PROMPT_FILE_BYTES = 10 * 1024 * 1024;
export const MAX_PROMPT_FILES_BYTES = 20 * 1024 * 1024;

export interface PromptFileLike {
  name: string;
  size: number;
  type: string;
  lastModified?: number;
}

export interface PromptFileDescriptor {
  key: string;
  name: string;
  size: number;
  mime: string;
  kind: 'document' | 'image';
  sourceIndex: number;
}

export interface PromptFileValidation {
  accepted: PromptFileDescriptor[];
  errors: string[];
}

const DOCUMENT_MIMES = new Set([
  'text/plain',
  'text/markdown',
  'text/csv',
  'application/json',
  'application/pdf',
]);
const IMAGE_MIMES = new Set(['image/png', 'image/jpeg', 'image/webp']);
const DOCUMENT_EXTENSIONS = new Set(['txt', 'md', 'markdown', 'csv', 'json', 'pdf']);
const IMAGE_EXTENSIONS = new Set(['png', 'jpg', 'jpeg', 'webp']);
const DANGEROUS_EXTENSIONS = new Set(['html', 'htm', 'svg', 'js', 'mjs', 'cjs', 'sh', 'exe', 'app', 'dmg']);

export function promptFileKey(file: PromptFileLike): string {
  return `${safePromptFilename(file.name)}:${file.size}:${file.lastModified ?? 0}`;
}

export function safePromptFilename(raw: string): string {
  const basename = raw.split(/[\\/]/).pop() ?? '';
  return basename.replace(/[\u0000-\u001F\u007F]/g, '').trim().slice(0, 160);
}

export function validatePromptFiles(
  files: readonly PromptFileLike[],
  existing: readonly Pick<PromptFileDescriptor, 'key' | 'size'>[] = [],
): PromptFileValidation {
  const accepted: PromptFileDescriptor[] = [];
  const errors: string[] = [];
  const seen = new Set(existing.map((file) => file.key));
  let totalBytes = existing.reduce((sum, file) => sum + file.size, 0);

  for (let sourceIndex = 0; sourceIndex < files.length; sourceIndex += 1) {
    const file = files[sourceIndex];
    const name = safePromptFilename(file.name);
    if (!name || file.size <= 0 || !Number.isFinite(file.size)) {
      errors.push(`${name || '未命名文件'}：文件为空或不可读`);
      continue;
    }
    if (existing.length + accepted.length >= MAX_PROMPT_FILES) {
      errors.push(`最多选择 ${MAX_PROMPT_FILES} 个文件`);
      break;
    }
    if (file.size > MAX_PROMPT_FILE_BYTES) {
      errors.push(`${name}：单个文件不能超过 10 MB`);
      continue;
    }
    if (totalBytes + file.size > MAX_PROMPT_FILES_BYTES) {
      errors.push('附件总大小不能超过 20 MB');
      break;
    }
    const extension = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1).toLowerCase() : '';
    const mime = file.type.trim().toLowerCase();
    if (DANGEROUS_EXTENSIONS.has(extension) || mime === 'text/html' || mime === 'image/svg+xml' || /javascript/.test(mime)) {
      errors.push(`${name}：不支持此文件类型`);
      continue;
    }
    const kind = IMAGE_MIMES.has(mime) || (!mime && IMAGE_EXTENSIONS.has(extension))
      ? 'image'
      : DOCUMENT_MIMES.has(mime) || (!mime && DOCUMENT_EXTENSIONS.has(extension))
        ? 'document'
        : null;
    if (!kind) {
      errors.push(`${name}：仅支持常用文档、PDF、PNG、JPEG 和 WebP`);
      continue;
    }
    const key = promptFileKey({ ...file, name });
    if (seen.has(key)) {
      errors.push(`${name}：已添加`);
      continue;
    }
    seen.add(key);
    totalBytes += file.size;
    accepted.push({ key, name, size: file.size, mime: mime || extensionMime(extension), kind, sourceIndex });
  }
  return { accepted, errors };
}

function extensionMime(extension: string): string {
  switch (extension) {
    case 'md':
    case 'markdown':
      return 'text/markdown';
    case 'csv':
      return 'text/csv';
    case 'json':
      return 'application/json';
    case 'pdf':
      return 'application/pdf';
    case 'png':
      return 'image/png';
    case 'jpg':
    case 'jpeg':
      return 'image/jpeg';
    case 'webp':
      return 'image/webp';
    default:
      return 'text/plain';
  }
}
