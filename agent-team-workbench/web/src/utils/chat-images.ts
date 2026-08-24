import type { UploadedImage } from '../api/endpoints';

export const CHAT_IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'] as const;
export const MAX_CHAT_IMAGES = 4;
export const MAX_CHAT_IMAGE_BYTES = 10 * 1024 * 1024;

export interface ImageSelectionResult {
  accepted: File[];
  rejected: string[];
}

export function validateChatImages(files: File[], occupied: number): ImageSelectionResult {
  const accepted: File[] = [];
  const rejected: string[] = [];
  let remaining = Math.max(0, MAX_CHAT_IMAGES - occupied);

  for (const file of files) {
    if (remaining === 0) {
      rejected.push(`${file.name}：最多上传 ${MAX_CHAT_IMAGES} 张图片`);
      continue;
    }
    if (!CHAT_IMAGE_TYPES.includes(file.type as (typeof CHAT_IMAGE_TYPES)[number])) {
      rejected.push(`${file.name}：仅支持 PNG、JPEG、WebP 或 GIF`);
      continue;
    }
    if (file.size > MAX_CHAT_IMAGE_BYTES) {
      rejected.push(`${file.name}：图片不能超过 10MB`);
      continue;
    }
    accepted.push(file);
    remaining--;
  }

  return { accepted, rejected };
}

export function buildImageInstruction(text: string, images: UploadedImage[]): string {
  const trimmed = text.trim();
  if (images.length === 0) return trimmed;
  const paths = images.map((image) => `- ${image.name}：${image.path}`).join('\n');
  const attachmentBlock = `请读取并分析以下本地图片附件：\n${paths}`;
  return trimmed ? `${trimmed}\n\n${attachmentBlock}` : attachmentBlock;
}
