import type { LucideIcon } from 'lucide-react';
import type { ReactNode } from 'react';

/** 配置工作台共享样式（对齐 ui-stack 8px 网格与设计 token） */
export const configLabelCls = 'block text-caption font-medium text-text-secondary mb-1.5';
export const configInputCls =
  'w-full rounded-input border border-border-strong bg-surface-raised px-snug py-2.5 text-body text-text-primary outline-none transition-shadow placeholder:text-text-tertiary/70 focus:border-brand-primary/50 focus:ring-2 focus:ring-brand-primary/20 disabled:opacity-50 disabled:cursor-not-allowed';

export function ConfigPage({ children }: { children: ReactNode }) {
  return <main className="config-page">{children}</main>;
}

export function ConfigPageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="config-page-header">
      <div className="flex items-center justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-h2 text-text-primary tracking-tight">{title}</h1>
          {subtitle ? <p className="text-caption text-text-tertiary mt-1 max-w-2xl">{subtitle}</p> : null}
        </div>
        {actions ? <div className="flex items-center gap-2 shrink-0">{actions}</div> : null}
      </div>
    </header>
  );
}

export function ConfigSplit({ children }: { children: ReactNode }) {
  return <div className="config-split">{children}</div>;
}

export function ConfigSidebar({
  title,
  children,
  footer,
}: {
  title: string;
  children: ReactNode;
  footer?: ReactNode;
}) {
  return (
    <aside className="config-sidebar">
      <div className="config-sidebar-heading">{title}</div>
      <div className="config-sidebar-scroll">{children}</div>
      {footer ? <div className="shrink-0 p-3 border-t border-border-subtle">{footer}</div> : null}
    </aside>
  );
}

export function ConfigSidebarItem({
  active,
  disabled,
  onClick,
  leading,
  title,
  subtitle,
}: {
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
  leading?: ReactNode;
  title: string;
  subtitle?: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={`config-sidebar-item ${active ? 'config-sidebar-item-active' : ''} ${disabled ? 'opacity-55' : ''}`}
    >
      {leading}
      <div className="min-w-0 flex-1 text-left">
        <div className="text-body font-medium text-text-primary truncate leading-snug">{title}</div>
        {subtitle ? <div className="text-caption text-text-tertiary truncate mt-0.5">{subtitle}</div> : null}
      </div>
    </button>
  );
}

export function ConfigMain({ children }: { children: ReactNode }) {
  return <section className="config-main">{children}</section>;
}

export function ConfigEmptyState({
  icon: Icon,
  title,
  hint,
}: {
  icon: LucideIcon;
  title: string;
  hint?: string;
}) {
  return (
    <div className="h-full flex items-center justify-center p-comfortable">
      <div className="text-center max-w-sm space-y-3">
        <div className="mx-auto w-14 h-14 rounded-2xl bg-surface-sunken border border-border-subtle flex items-center justify-center">
          <Icon className="w-7 h-7 text-text-tertiary" strokeWidth={1.5} />
        </div>
        <p className="text-body font-medium text-text-secondary">{title}</p>
        {hint ? <p className="text-caption text-text-tertiary">{hint}</p> : null}
      </div>
    </div>
  );
}

export function ConfigPanel({ children }: { children: ReactNode }) {
  return <div className="config-panel-wrap">{children}</div>;
}

export function ConfigFormCard({ children }: { children: ReactNode }) {
  return <div className="config-form-card">{children}</div>;
}

export function ConfigSection({
  title,
  hint,
  children,
  noPadding,
}: {
  title?: string;
  hint?: string;
  children: ReactNode;
  noPadding?: boolean;
}) {
  return (
    <section className={noPadding ? 'config-section-flat' : 'config-section'}>
      {title ? (
        <div className="config-section-head">
          <h2 className="config-section-title">{title}</h2>
          {hint ? <p className="config-section-hint">{hint}</p> : null}
        </div>
      ) : null}
      {children}
    </section>
  );
}

export function ConfigToolbar({ children }: { children: ReactNode }) {
  return <div className="config-toolbar">{children}</div>;
}

export function ConfigFooter({
  onCancel,
  onSave,
  saving,
  saveDisabled,
  saveLabel = '保存',
}: {
  onCancel?: () => void;
  onSave: () => void;
  saving?: boolean;
  saveDisabled?: boolean;
  saveLabel?: string;
}) {
  return (
    <div className="config-footer">
      <div className="config-footer-inner">
        {onCancel ? (
          <button type="button" onClick={onCancel} className="btn-secondary">
            取消
          </button>
        ) : (
          <span />
        )}
        <button
          type="button"
          onClick={onSave}
          disabled={saving || saveDisabled}
          className="btn-primary min-w-[88px]"
        >
          {saving ? '保存中…' : saveLabel}
        </button>
      </div>
    </div>
  );
}

export function ConfigToolGrid({
  options,
  selected,
  onToggle,
}: {
  options: { value: string; label: string }[];
  selected: string[];
  onToggle: (value: string) => void;
}) {
  return (
    <div className="config-tool-grid">
      {options.map((t) => {
        const checked = selected.includes(t.value);
        return (
          <label key={t.value} className={`config-tool-chip ${checked ? 'config-tool-chip-active' : ''}`}>
            <input
              type="checkbox"
              checked={checked}
              onChange={() => onToggle(t.value)}
              className="sr-only"
            />
            <span
              className={`config-tool-check ${checked ? 'config-tool-check-active' : ''}`}
              aria-hidden
            />
            <span className="text-body text-text-primary">{t.label}</span>
          </label>
        );
      })}
    </div>
  );
}

export function ConfigStatusDot({ active, title }: { active: boolean; title?: string }) {
  return (
    <span
      className={`w-2 h-2 rounded-full shrink-0 ${active ? 'bg-status-success' : 'bg-text-tertiary/50'}`}
      title={title}
    />
  );
}

export function ConfigAvatar({ label, tone = 'brand' }: { label: string; tone?: 'brand' | 'muted' }) {
  const letter = label.trim().slice(0, 1).toUpperCase() || '?';
  return (
    <span
      className={`config-avatar ${tone === 'brand' ? 'config-avatar-brand' : 'config-avatar-muted'}`}
      aria-hidden
    >
      {letter}
    </span>
  );
}
