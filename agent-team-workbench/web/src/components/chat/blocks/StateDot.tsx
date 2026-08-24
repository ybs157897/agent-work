import css from './StateDot.module.css';
import { cx } from './cx';

/**
 * 会话状态指示点：done/warning/error 是 10x10 的同色外晕 + 6x6 实心核；
 * ongoing 是 3x3 像素矩阵的顺时针追逐动画（SVG 逐格 rect）。
 * aria-hidden，无障碍语义由配对的文本承担。
 */

/** 四态语义：绿 done / 琥珀 warning / 蓝转圈 ongoing / 红 error。 */
export type StateDotState = 'done' | 'warning' | 'ongoing' | 'error';

/** 3x3 矩阵的外圈格（10px 网格上的 2px 像素），从左上起顺时针排列。 */
const MATRIX_CELLS: readonly (readonly [number, number])[] = [
  [0, 0], [4, 0], [8, 0], [8, 4], [8, 8], [4, 8], [0, 8], [0, 4],
];

/**
 * 渲染一个状态点。
 * @param props.state 四态之一。
 * @param props.size 外径（px），默认 10。
 * @param props.className 布局用的附加 class。
 */
export function StateDot({ state, size = 10, className }: {
  state: StateDotState;
  size?: number | undefined;
  className?: string | undefined;
}): JSX.Element {
  if (state === 'ongoing') {
    return (
      <svg
        className={cx(css.matrix, className)}
        data-state="ongoing"
        width={size}
        height={size}
        viewBox="0 0 10 10"
        shapeRendering="crispEdges"
        aria-hidden="true"
      >
        {MATRIX_CELLS.map(([x, y], index) => (
          <rect
            key={`${x}-${y}`}
            className={css.cell}
            x={x}
            y={y}
            width="2"
            height="2"
            // 负延迟错开相位，让每格从挂载起就处于追逐的不同步位
            style={{ animationDelay: `${(index - MATRIX_CELLS.length) * 125}ms` }}
          />
        ))}
      </svg>
    );
  }
  return (
    <span
      className={cx(css.dot, className)}
      data-state={state}
      style={{ width: size, height: size }}
      aria-hidden="true"
    />
  );
}
