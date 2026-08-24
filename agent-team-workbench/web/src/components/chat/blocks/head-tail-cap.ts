/**
 * 头尾高度帽的切分算术（TerminalBlock / SearchBlock 共用）：超帽且未展开时
 * 展示头尾两段——头段 `ceil(maxLines / 2)` 行，其余给尾段；未超帽全量展示。
 * 纯计算，由调用方用 headLines/tailLines 自行切片，块组件可在其上叠加
 * 自己的关切（如 SearchBlock 恢复尾段的文件头行）。
 */

/** 一个被截断列表的头尾切分度量。 */
export interface HeadTailCap {
  /** 超出帽子的行数（总行数 − maxLines）；≤ 0 表示没有行被隐藏。 */
  hidden: number;
  /** 是否处于「超帽且未展开」状态，此时展示头尾切片。 */
  capped: boolean;
  /** 头段行数：`ceil(maxLines / 2)`。 */
  headLines: number;
  /** 尾段行数：帽子余量（maxLines − headLines）。 */
  tailLines: number;
}

/**
 * 对 total 行的列表按 maxLines 帽子计算头尾切分度量。
 * @param total 列表总行数。
 * @param maxLines 折叠态高度帽（行数）。
 * @param expanded 当前是否展开（展开即解除帽子）。
 */
export function headTailCap(total: number, maxLines: number, expanded: boolean): HeadTailCap {
  const hidden = total - maxLines;
  const headLines = Math.ceil(maxLines / 2);
  return { hidden, capped: hidden > 0 && !expanded, headLines, tailLines: maxLines - headLines };
}
