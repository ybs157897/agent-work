import { describe, expect, it } from 'vitest';
import {
  MAX_PROMPT_FILE_BYTES,
  MAX_PROMPT_FILES,
  promptFileKey,
  safePromptFilename,
  validatePromptFiles,
  type PromptFileLike,
} from './prompt-files';

const file = (name: string, type = '', size = 120, lastModified = 1): PromptFileLike => ({ name, type, size, lastModified });

describe('prompt file validation', () => {
  it('accepts bounded documents and images with deterministic keys', () => {
    const result = validatePromptFiles([
      file('notes.md', 'text/markdown'),
      file('photo.webp', 'image/webp'),
      file('fallback.csv'),
    ]);
    expect(result.errors).toEqual([]);
    expect(result.accepted.map((item) => [item.name, item.mime, item.kind])).toEqual([
      ['notes.md', 'text/markdown', 'document'],
      ['photo.webp', 'image/webp', 'image'],
      ['fallback.csv', 'text/csv', 'document'],
    ]);
    expect(result.accepted[0].key).toBe(promptFileKey(file('notes.md', 'text/markdown')));
  });

  it('rejects dangerous, unknown, empty and oversized files', () => {
    const result = validatePromptFiles([
      file('payload.svg', 'image/svg+xml'),
      file('page.html', 'text/html'),
      file('run.sh'),
      file('archive.zip', 'application/zip'),
      file('empty.txt', 'text/plain', 0),
      file('huge.pdf', 'application/pdf', MAX_PROMPT_FILE_BYTES + 1),
    ]);
    expect(result.accepted).toEqual([]);
    expect(result.errors).toHaveLength(6);
  });

  it('deduplicates and enforces count/total limits without trusting paths', () => {
    const first = file('../private/notes.txt', 'text/plain', 5 * 1024 * 1024, 7);
    const firstDescriptor = validatePromptFiles([first]).accepted[0];
    expect(firstDescriptor.name).toBe('notes.txt');
    expect(safePromptFilename('a\\b\u0000.txt')).toBe('b.txt');

    const result = validatePromptFiles([
      first,
      file('two.txt', 'text/plain', 5 * 1024 * 1024),
      file('three.txt', 'text/plain', 5 * 1024 * 1024),
      file('four.txt', 'text/plain', 5 * 1024 * 1024),
      file('five.txt', 'text/plain', 1),
    ], [firstDescriptor]);
    expect(result.accepted.length).toBeLessThanOrEqual(MAX_PROMPT_FILES - 1);
    expect(result.errors.some((error) => error.includes('已添加'))).toBe(true);
    expect(result.errors.some((error) => error.includes('20 MB') || error.includes('最多选择'))).toBe(true);
  });
});
