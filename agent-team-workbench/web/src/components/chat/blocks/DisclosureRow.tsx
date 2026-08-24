import { ChevronDown } from 'lucide-react';
import { type KeyboardEvent, type MouseEvent, type ReactNode } from 'react';
import css from './DisclosureRow.module.css';
import { cx } from './cx';

/**
 * 紧凑流式行的共享 24px 披露 chrome：前导槽在 collapsed 时显图标、hover 时
 * 图标淡出换 chevron 预览（previewChevron）、open 时恒为 chevron；整行点击
 * 切换由 expandOnRowClick 控制（含 Enter/Space 键盘触发），否则前导槽自身
 * 是切换按钮。
 */

/** 披露行的 props：视觉内容、受控状态与交互策略。 */
export interface DisclosureRowProps {
  icon: ReactNode;
  title: string;
  open: boolean;
  expandable: boolean;
  onToggle: () => void;
  /** 让整个标题行成为披露目标。 */
  expandOnRowClick?: boolean | undefined;
  /** 行 hover 时把折叠图标换成 chevron 预览。 */
  previewChevron?: boolean | undefined;
  /** open 时仍保留 collapsedContent。 */
  keepContentWhenOpen?: boolean | undefined;
  collapsedContent?: ReactNode;
  children?: ReactNode;
  className?: string | undefined;
  rowClassName?: string | undefined;
  leadingClassName?: string | undefined;
  chevronClassName?: string | undefined;
  titleClassName?: string | undefined;
}

export function DisclosureRow({
  icon,
  title,
  open,
  expandable,
  onToggle,
  expandOnRowClick = false,
  previewChevron = expandable,
  keepContentWhenOpen = false,
  collapsedContent,
  children,
  className,
  rowClassName,
  leadingClassName,
  chevronClassName,
  titleClassName,
}: DisclosureRowProps) {
  const rowExpands = expandable && expandOnRowClick;
  const toggleFromLeading = (event: MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    onToggle();
  };
  const toggleFromKeyboard = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!rowExpands || (event.key !== 'Enter' && event.key !== ' ')) return;
    event.preventDefault();
    onToggle();
  };
  const collapsedLeading = previewChevron
    ? (
      <>
        <span className={css.iconIdle}>{icon}</span>
        <ChevronDown size={14} className={cx(chevronClassName, css.chevronHover)} />
      </>
    )
    : icon;
  const leading = open
    ? <ChevronDown size={14} className={chevronClassName} />
    : collapsedLeading;

  return (
    <div className={cx(css.root, className)} data-open={open || undefined}>
      <div
        className={cx(css.row, rowClassName)}
        data-disclosure-row
        data-expandable={rowExpands || undefined}
        role={rowExpands ? 'button' : undefined}
        tabIndex={rowExpands ? 0 : undefined}
        aria-expanded={rowExpands ? open : undefined}
        onClick={rowExpands ? onToggle : undefined}
        onKeyDown={rowExpands ? toggleFromKeyboard : undefined}
      >
        {expandable && !rowExpands ? (
          <button
            type="button"
            className={cx(css.leading, leadingClassName)}
            aria-expanded={open}
            onClick={toggleFromLeading}
          >
            {leading}
          </button>
        ) : (
          <span className={cx(css.leading, leadingClassName)}>
            {leading}
          </span>
        )}
        <span className={cx(css.title, titleClassName)}>{title}</span>
        {(keepContentWhenOpen || !open) && collapsedContent}
      </div>
      {open && children}
    </div>
  );
}
