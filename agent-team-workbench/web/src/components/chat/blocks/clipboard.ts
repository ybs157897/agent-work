/**
 * 写入剪贴板：优先 navigator.clipboard.writeText，不可用或被拒绝（权限/iframe
 * 策略）时降级 execCommand('copy')；两条路都不通返回 false，由调用方决定静默处理。
 * 注意 lib.dom 把 navigator.clipboard 声明为非可选，但非安全上下文 / 测试环境
 * 运行时可能缺失，这里的可选链探测的正是这个运行时缺口。
 */
export async function writeClipboard(text: string): Promise<boolean> {
  const clipboard = typeof navigator !== 'undefined' ? navigator.clipboard : undefined;
  if (clipboard?.writeText) {
    try {
      await clipboard.writeText(text);
      return true;
    } catch {
      // 权限拒绝等失败 → 落到 execCommand 兜底，而不是直接宣告失败
    }
  }
  return legacyCopy(text);
}

/** execCommand 已废弃，但它是异步 Clipboard API 缺席时唯一可用的兜底路径。 */
function legacyCopy(text: string): boolean {
  if (typeof document === 'undefined') return false;
  const exec = typeof document.execCommand === 'function' ? document.execCommand.bind(document) : undefined;
  if (exec === undefined) return false;
  const el = document.createElement('textarea');
  el.value = text;
  el.setAttribute('readonly', '');
  el.style.position = 'fixed';
  el.style.left = '-9999px';
  document.body.appendChild(el);
  el.select();
  try {
    return exec('copy');
  } catch {
    return false;
  } finally {
    el.remove();
  }
}
