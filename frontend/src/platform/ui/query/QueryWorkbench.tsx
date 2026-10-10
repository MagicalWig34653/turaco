import { useId, useRef, useState } from 'react';
import { useI18n } from '../../i18n/I18nProvider';
import type { MessageKey } from '../../i18n/i18n';
import { Alert } from '../Alert';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { Button } from '../Button';
import { useContextMenu, type MenuItem } from '../ContextMenu';
import type { Column } from '../DataTable';
import { ColumnChooser } from './ColumnChooser';
import { GroupEditor } from './ConditionTree';
import {
  asGroup,
  duplicateAt,
  emptyState,
  limitOfError,
  listConditions,
  negate,
  normalize,
  replaceAt,
  validate,
  type Node,
  type QueryState,
} from './filterModel';
import { useQueryLabels } from './queryLabels';
import { referenceKind, useReferenceNames } from './referenceNames';
import { SortEditor } from './SortEditor';
import { ViewBar } from '../views/ViewBar';
import type { QueryList } from './useQueryList';
import './query.css';

type Props<T> = {
  query: QueryList<T>;
  columns: readonly Column<T>[];
  /** Storage key for the per-list column layout. */
  listKey: string;
  onColumnsChange: (keys: string[]) => void;
};

/**
 * Click-together filter builder for a server-side query list: active-filter chips, a draft panel
 * with nested AND/OR groups, multi-column sort, a full-text box and the column chooser.
 * The catalog decides which fields and operators exist; the server stays the authority.
 */
export function QueryWorkbench<T>({ query, columns, listKey, onColumnsChange }: Props<T>) {
  const { t } = useI18n();
  const panelId = useId();
  const panel = useRef<HTMLDivElement>(null);
  const opener = useRef<HTMLButtonElement>(null);
  const menu = useContextMenu();
  const { catalog } = query;
  const labels = useQueryLabels(catalog);
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState<QueryState>(query.state);
  const [undo, setUndo] = useState<QueryState>();
  const [visibleColumns, setVisibleColumns] = useState<string[]>();

  const active = listConditions(query.state.filter.root);
  const resolve = useReferenceNames(
    active.flatMap(({ node }) => {
      const kind = referenceKind(
        catalog?.fields.find((field) => field.key === node.field)?.reference,
      );
      const ids = (Array.isArray(node.value) ? node.value : [node.value]).filter(
        (id): id is string => typeof id === 'string' && id !== '',
      );
      return kind ? ids.map((id) => ({ kind, id })) : [];
    }),
  );

  const commit = (next: QueryState) => {
    setUndo(query.state);
    query.setState(next);
    setDraft(next);
  };
  const edit = () => {
    setDraft(query.state);
    setOpen(true);
    requestAnimationFrame(() =>
      panel.current?.querySelector<HTMLElement>('input, select, button')?.focus(),
    );
  };
  const close = () => {
    setOpen(false);
    opener.current?.focus();
  };
  const changeAt = (path: number[], replacement?: Node) => {
    const root = query.state.filter.root;
    if (!root) return;
    const next = normalize(replaceAt(root, path, replacement));
    commit({ ...query.state, filter: next ? { v: 1, root: next } : { v: 1 } });
  };
  const apply = () => {
    const root = normalize(draft.filter.root);
    commit({ ...draft, search: draft.search.trim(), filter: root ? { v: 1, root } : { v: 1 } });
    close();
  };

  const draftErrors = catalog
    ? validate(
        {
          ...draft,
          filter: {
            v: 1,
            ...(normalize(draft.filter.root) ? { root: normalize(draft.filter.root) as Node } : {}),
          },
        },
        catalog,
      )
    : [];
  const limits = catalog?.limits ?? {};

  const chipActions = (index: number): MenuItem[] => {
    const entry = active[index];
    if (!entry) return [];
    const field = catalog?.fields.find((candidate) => candidate.key === entry.node.field);
    const inverse = field ? negate(entry.node, field) : undefined;
    const root = query.state.filter.root;
    return [
      { id: 'edit', label: t('query.edit'), onSelect: edit },
      {
        id: 'duplicate',
        label: t('query.duplicate'),
        onSelect: () =>
          root && commit({ ...query.state, filter: { v: 1, root: duplicateAt(root, entry.path) } }),
      },
      inverse
        ? { id: 'negate', label: t('query.negate'), onSelect: () => changeAt(entry.path, inverse) }
        : {
            id: 'negate',
            label: t('query.negate'),
            disabledReason: t('query.cannotNegate'),
            onSelect: () => undefined,
          },
      {
        id: 'remove',
        label: t('query.remove'),
        danger: true,
        onSelect: () => changeAt(entry.path),
      },
    ];
  };

  const countText =
    !query.list.loading && !query.list.error && query.count !== undefined
      ? t('query.count', { count: `${query.count}${query.countCapped ? '+' : ''}` })
      : t('query.countPending');

  return (
    <section className="query-workbench" aria-label={t('query.title')}>
      <div className="query-bar">
        <Button
          ref={opener}
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => (open ? close() : edit())}
        >
          {t('query.title')}
        </Button>
        <Button
          disabled={!undo}
          onClick={() => {
            if (!undo) return;
            const previous = query.state;
            query.setState(undo);
            setDraft(undo);
            setUndo(previous);
          }}
        >
          {t('query.undo')}
        </Button>
        <Button
          disabled={active.length === 0 && !query.state.search && query.state.sort.length === 0}
          onClick={() => commit(emptyState())}
        >
          {t('query.clear')}
        </Button>
        <span role="status" className="query-count">
          {countText}
        </span>
      </div>

      {query.viewsEnabled ? (
        <ViewBar
          query={query}
          visibleColumns={visibleColumns}
          offeredColumns={columns.map((column) => column.key)}
        />
      ) : null}

      {query.urlError ? (
        <Alert kind="warning">
          <p>{t('query.urlInvalid')}</p>
          <Button onClick={() => commit(emptyState())}>{t('query.clear')}</Button>
        </Alert>
      ) : null}

      {active.length > 0 || query.state.search ? (
        <ul className="active-filters" aria-label={t('query.activeFilters')}>
          {active.map(({ node, path }, index) => {
            const field = catalog?.fields.find((candidate) => candidate.key === node.field);
            const text = labels.describe(node, field, resolve);
            return (
              <li key={path.join('.')} className="filter-chip-item">
                <button
                  type="button"
                  className="filter-chip"
                  title={t('query.chipHint')}
                  onClick={edit}
                  onContextMenu={(event) => {
                    event.preventDefault();
                    menu.openAtPoint(
                      chipActions(index),
                      { x: event.clientX, y: event.clientY },
                      event.currentTarget,
                      text,
                    );
                  }}
                  onKeyDown={(event) => {
                    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                      event.preventDefault();
                      menu.openAtElement(chipActions(index), event.currentTarget, text);
                    }
                  }}
                >
                  {text}
                </button>
                <button
                  type="button"
                  className="filter-chip-remove"
                  aria-label={t('query.removeFilter', { name: text })}
                  onClick={() => changeAt(path)}
                >
                  ×
                </button>
              </li>
            );
          })}
          {query.state.search ? (
            <li className="filter-chip-item">
              <span className="filter-chip">
                {t('query.searchChip', { text: query.state.search })}
              </span>
              <button
                type="button"
                className="filter-chip-remove"
                aria-label={t('query.removeSearch')}
                onClick={() => commit({ ...query.state, search: '' })}
              >
                ×
              </button>
            </li>
          ) : null}
        </ul>
      ) : null}
      {menu.menu}
      {query.catalogError ? <ApiErrorAlert error={query.catalogError} /> : null}

      {open ? (
        <div
          id={panelId}
          ref={panel}
          className="query-panel"
          role="group"
          aria-label={t('query.panelLabel')}
          onKeyDown={(event) => {
            if (event.key === 'Escape') {
              event.stopPropagation();
              close();
            }
          }}
        >
          {!catalog ? (
            <p role="status">{t('state.loading')}</p>
          ) : (
            <>
              <label>
                <span>{t('query.searchAll')}</span>
                <input
                  type="search"
                  value={draft.search}
                  maxLength={limits.maxSearchLength ?? 100}
                  placeholder={t('query.searchAllHint')}
                  onChange={(event) => setDraft({ ...draft, search: event.target.value })}
                />
              </label>
              <GroupEditor
                node={asGroup(draft.filter.root)}
                catalog={catalog}
                onChange={(root) => setDraft({ ...draft, filter: { v: 1, root } })}
              />
              <SortEditor
                catalog={catalog}
                sort={draft.sort}
                onChange={(sort) => setDraft({ ...draft, sort })}
              />
              {draftErrors.length > 0 ? (
                <div role="alert" className="query-errors">
                  {draftErrors.map((error) => (
                    <p key={error}>
                      {t(error as MessageKey, {
                        limit:
                          limits[limitOfError[error.replace('query.validation.', '')] ?? ''] ?? '',
                      })}
                    </p>
                  ))}
                </div>
              ) : null}
              <p className="field-hint">
                {t('query.limits', {
                  conditions: limits.maxConditions ?? 25,
                  depth: limits.maxDepth ?? 4,
                })}
              </p>
              <div className="query-actions">
                <Button variant="primary" disabled={draftErrors.length > 0} onClick={apply}>
                  {t('query.apply')}
                </Button>
                <Button onClick={close}>{t('query.cancel')}</Button>
              </div>
            </>
          )}
        </div>
      ) : null}
      <ColumnChooser
        listKey={listKey}
        columns={columns.map((column) => ({ key: column.key, header: column.header }))}
        onChange={(keys) => {
          setVisibleColumns(keys);
          onColumnsChange(keys);
        }}
        applied={{ id: query.viewApplied, keys: query.view?.definition.columns ?? [] }}
      />
    </section>
  );
}
