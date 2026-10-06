import { Children, isValidElement, useId, type FormHTMLAttributes, type ReactNode } from 'react';
import { useI18n } from '../i18n/I18nProvider';
import { TextField } from './Field';

export type ActiveFilter = { key: string; label: string; onRemove: () => void };

/** Controlled by the screen: filtering and URL semantics stay with their existing owner. */
export function FilterBar({
  children,
  secondary,
  activeFilters = [],
  onClear,
  primaryCount = 4,
  className = '',
  ...props
}: Omit<FormHTMLAttributes<HTMLFormElement>, 'onReset'> & {
  secondary?: ReactNode;
  activeFilters?: readonly ActiveFilter[];
  onClear?: () => void;
  primaryCount?: number;
}) {
  const { t } = useI18n();
  const nodes = Children.toArray(children);
  const isSubmit = (node: ReactNode): boolean =>
    isValidElement<{ type?: string; children?: ReactNode }>(node) &&
    (node.props.type === 'submit' || Children.toArray(node.props.children).some(isSubmit));
  const controls = nodes.filter((node) => !isSubmit(node));
  const actions = nodes.filter(isSubmit);
  const overflow = controls.slice(primaryCount);
  return (
    <form
      {...props}
      className={`collection-filters ${className}`}
      aria-label={props['aria-label'] ?? t('filters.title')}
      onSubmit={props.onSubmit ?? ((event) => event.preventDefault())}
    >
      <div className="collection-filter-row">
        {controls.slice(0, primaryCount)}
        {actions}
        {secondary || overflow.length > 0 ? (
          <details
            className="filter-disclosure"
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.currentTarget.open = false;
                event.currentTarget.querySelector('summary')?.focus();
              }
            }}
          >
            <summary>
              <span aria-hidden="true">☷</span>
              {t('table.allFilters')}
              {activeFilters.length > 0 ? (
                <span className="filter-count">{activeFilters.length}</span>
              ) : null}
            </summary>
            <div className="filter-overflow">
              {overflow}
              {secondary}
            </div>
          </details>
        ) : null}
      </div>
      {activeFilters.length > 0 ? (
        <div className="active-filters" aria-label={t('table.activeFilters')}>
          <span className="active-filters-label">{t('table.filtered')}</span>
          {activeFilters.map((filter) => (
            <button
              key={filter.key}
              type="button"
              className="filter-chip"
              onClick={filter.onRemove}
              aria-label={t('table.removeFilter', { filter: filter.label })}
            >
              {filter.label}
              <span aria-hidden="true">×</span>
            </button>
          ))}
          <button
            type="button"
            className="filter-clear"
            onClick={() => {
              if (onClear) onClear();
              else activeFilters.forEach((filter) => filter.onRemove());
            }}
          >
            {t('table.clearFilters')}
          </button>
        </div>
      ) : null}
    </form>
  );
}

/** A quiet formatted trigger; native date editing stays available inside the disclosure. */
export function DateFilter({
  label,
  value,
  onChange,
  type = 'datetime-local',
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: 'date' | 'datetime-local';
}) {
  const { t, locale } = useI18n();
  const id = useId();
  const date = value ? new Date(type === 'date' ? `${value}T00:00:00` : value) : null;
  const formatted =
    date && Number.isFinite(date.getTime())
      ? new Intl.DateTimeFormat(locale, {
          dateStyle: 'medium',
          ...(type === 'datetime-local' ? { timeStyle: 'short' as const } : {}),
        }).format(date)
      : t('table.anyDate');
  return (
    <details className="date-filter">
      <summary aria-controls={id}>
        <span className="date-filter-label">{label}</span>
        <span>{formatted}</span>
        <span aria-hidden="true">⌄</span>
      </summary>
      <div id={id} className="date-filter-editor">
        <TextField
          type={type}
          label={label}
          value={value}
          onChange={(event) => onChange(event.target.value)}
        />
      </div>
    </details>
  );
}

export function SegmentedFilter({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: readonly { value: string; label: string; count?: number }[];
  onChange: (value: string) => void;
}) {
  return (
    <div className="filter-segments" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          type="button"
          key={option.value}
          aria-pressed={value === option.value}
          onClick={() => onChange(option.value)}
        >
          {option.label}
          {option.count !== undefined ? <span className="filter-count">{option.count}</span> : null}
        </button>
      ))}
    </div>
  );
}
