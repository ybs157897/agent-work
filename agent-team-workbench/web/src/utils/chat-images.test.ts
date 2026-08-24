import { describe, expect, it } from 'vitest';
import { buildImageInstruction, MAX_CHAT_IMAGE_BYTES, validateChatImages } from './chat-images';

const image = (name: string, type: string, size: number) => ({ name, type, size }) as File;

describe('chat image helpers', () => {
  it('只接受受支持且不超过限制的图片，并遵守总数上限', () => {
    const result = validateChatImages([
      image('ok.png', 'image/png', 1024),
      image('large.jpg', 'image/jpeg', MAX_CHAT_IMAGE_BYTES + 1),
      image('note.svg', 'image/svg+xml', 100),
      image('extra.webp', 'image/webp', 100),
    ], 2);

    expect(result.accepted.map((file) => file.name)).toEqual(['ok.png', 'extra.webp']);
    expect(result.rejected).toEqual([
      'large.jpg：图片不能超过 10MB',
      'note.svg：仅支持 PNG、JPEG、WebP 或 GIF',
    ]);
  });

  it('将用户正文与 Agent 可读取的绝对图片路径合并为一条指令', () => {
    expect(buildImageInstruction('分析布局', [
      { name: 'screen.png', mime: 'image/png', size: 120, path: '/workspace/uploads/screen.png' },
    ])).toBe('分析布局\n\n请读取并分析以下本地图片附件：\n- screen.png：/workspace/uploads/screen.png');
  });
});
