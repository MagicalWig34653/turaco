import { useEffect, useId, useState, type ReactNode } from 'react';
import { Link } from '../router/Router';
import { useI18n } from '../i18n/I18nProvider';
import { Badge } from './Alert';

export function Card({
  children,
  className = '',
  title,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <section className={`workspace-card ${className}`} aria-label={title}>
      {children}
    </section>
  );
}

export function useCountUp(value: number, enabled = true): number {
  const [display, setDisplay] = useState(0);
  useEffect(() => {
    if (
      !enabled ||
      window.matchMedia('(prefers-reduced-motion: reduce)').matches ||
      document.documentElement.dataset.motion === 'reduced'
    )
      return;
    let frame = 0;
    const start = performance.now();
    const from = 0;
    const tick = (now: number) => {
      if (
        window.matchMedia('(prefers-reduced-motion: reduce)').matches ||
        document.documentElement.dataset.motion === 'reduced'
      ) {
        setDisplay(value);
        return;
      }
      const progress = Math.min(1, (now - start) / 550);
      setDisplay(Math.round(from + (value - from) * (1 - Math.pow(1 - progress, 3))));
      if (progress < 1) frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [value, enabled]);
  return enabled &&
    typeof window !== 'undefined' &&
    !window.matchMedia('(prefers-reduced-motion: reduce)').matches &&
    document.documentElement.dataset.motion !== 'reduced'
    ? display
    : value;
}

export function MetricCard({
  label,
  value,
  to,
  tone = 'info',
  caption,
  denominator,
  icon,
}: {
  label: string;
  value: number;
  to: string;
  tone?: 'info' | 'warning' | 'danger' | 'success';
  caption?: string;
  denominator?: number;
  icon?: ReactNode;
}) {
  const display = useCountUp(value);
  return (
    <Link
      className={`metric-card metric-${tone}`}
      to={to}
      aria-label={`${label}: ${value}${denominator !== undefined ? ` / ${denominator}` : ''}${caption ? ` · ${caption}` : ''}`}
    >
      <span className="metric-label">{label}</span>
      <strong aria-hidden="true">
        {display}
        {denominator !== undefined ? (
          <small className="metric-denominator"> / {denominator}</small>
        ) : null}
      </strong>
      {caption ? <span className="metric-caption">{caption}</span> : null}
      <span className="metric-arrow" aria-hidden="true">
        {icon ?? '↗'}
      </span>
    </Link>
  );
}

export function StatusBadge({
  tone,
  children,
}: {
  tone: 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'unknown';
  children: ReactNode;
}) {
  return <Badge tone={tone}>{children}</Badge>;
}

export function Toolbar({ children }: { children: ReactNode }) {
  return <div className="workspace-toolbar">{children}</div>;
}
export { FilterBar } from './FilterBar';

export function EmptyState({
  title,
  description,
  action,
}: {
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="workspace-empty">
      <span aria-hidden="true">✦</span>
      <h2>{title}</h2>
      {description ? <p>{description}</p> : null}
      {action}
    </div>
  );
}

export function Skeleton({ lines = 3 }: { lines?: number }) {
  const { t } = useI18n();
  return (
    <div role="status" aria-label={t('state.loading')} className="workspace-skeleton">
      {Array.from({ length: lines }, (_, index) => (
        <span key={index} />
      ))}
    </div>
  );
}

export function Toast({
  children,
  kind = 'info',
}: {
  children: ReactNode;
  kind?: 'info' | 'success' | 'error';
}) {
  return (
    <div role={kind === 'error' ? 'alert' : 'status'} className={`workspace-toast toast-${kind}`}>
      {children}
    </div>
  );
}

export function Avatar({ name }: { name?: string | null }) {
  const initials =
    name
      ?.match(/\p{L}+/gu)
      ?.slice(0, 2)
      .map((part) => part[0]?.toUpperCase())
      .join('') || '👤';
  return (
    <span className="workspace-avatar" aria-hidden="true">
      {initials}
    </span>
  );
}

export function Tabs({
  items,
  active,
  onChange,
  idPrefix,
}: {
  items: readonly { id: string; label: string }[];
  active: string;
  onChange: (id: string) => void;
  idPrefix?: string;
}) {
  return (
    <div className="workspace-tabs" role="tablist">
      {items.map((item) => (
        <button
          key={item.id}
          type="button"
          role="tab"
          aria-selected={active === item.id}
          aria-controls={idPrefix ? `${idPrefix}-panel-${item.id}` : undefined}
          id={idPrefix ? `${idPrefix}-tab-${item.id}` : undefined}
          tabIndex={active === item.id ? 0 : -1}
          onKeyDown={(event) => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
            event.preventDefault();
            const index = items.findIndex((entry) => entry.id === item.id);
            const next =
              event.key === 'Home'
                ? 0
                : event.key === 'End'
                  ? items.length - 1
                  : (index + (event.key === 'ArrowRight' ? 1 : -1) + items.length) % items.length;
            const nextItem = items[next];
            if (nextItem) {
              onChange(nextItem.id);
              event.currentTarget.parentElement
                ?.querySelectorAll<HTMLButtonElement>('button')
                [next]?.focus();
            }
          }}
          onClick={() => onChange(item.id)}
        >
          {item.label}
        </button>
      ))}
    </div>
  );
}

export function SplitPane({
  main,
  inspector,
  inspectorLabel,
}: {
  main: ReactNode;
  inspector: ReactNode;
  inspectorLabel: string;
}) {
  const { t } = useI18n();
  const idPrefix = useId();
  const [view, setView] = useState<'list' | 'inspector'>('list');
  return (
    <>
      <div className="workspace-mobile-switch">
        <Tabs
          active={view}
          idPrefix={idPrefix}
          onChange={(next) => setView(next as 'list' | 'inspector')}
          items={[
            { id: 'list', label: t('workspace.list') },
            { id: 'inspector', label: inspectorLabel },
          ]}
        />
      </div>
      <div className={`workspace-split workspace-view-${view}`}>
        <div
          className="workspace-main"
          role="tabpanel"
          id={`${idPrefix}-panel-list`}
          aria-labelledby={`${idPrefix}-tab-list`}
        >
          {main}
        </div>
        <aside
          className="workspace-inspector"
          role="tabpanel"
          id={`${idPrefix}-panel-inspector`}
          aria-labelledby={`${idPrefix}-tab-inspector`}
          aria-label={inspectorLabel}
        >
          {inspector}
        </aside>
      </div>
    </>
  );
}
