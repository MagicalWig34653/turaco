import { useEffect, useRef, useState, isValidElement, type ReactNode } from 'react';
import { Link, navigate } from '../router/Router';
import { sortRows, type SortValue } from './tableModel';
import { EmptyState } from './Workspace';
import type { ApiError } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from './Button';
import { ApiErrorAlert } from './ApiErrorAlert';
import { useContextMenu, copyContextText, type MenuItem } from './ContextMenu';
import { useHorizontalOverflow } from './hooks';

export type Column<T> = {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  className?: string;
  sortValue?: (row: T) => SortValue;
};

type DataTableProps<T> = {
  caption: string;
  totalCount?: number;
  filterSummary?: string;
  zebra?: boolean;
  selection?: {
    keys: ReadonlySet<string>;
    onChange: (keys: Set<string>) => void;
    label: (row: T) => string;
    actions: ReactNode;
  };

  columns: readonly Column<T>[];
  rows: readonly T[];
  rowKey: (row: T) => string;
  loading?: boolean;
  error?: ApiError | undefined;
  onRetry?: () => void;
  emptyText: string;
  /** Pagination ("load more" via nextCursor). */
  hasMore?: boolean;
  loadingMore?: boolean;
  loadMoreError?: ApiError | undefined;
  onLoadMore?: () => void;
  /** Actions available for this row. Passing a function adds a keyboard and touch-accessible menu. */
  rowActions?: (row: T) => MenuItem[];
};

export function DataTable<T>({
  caption,
  totalCount,
  filterSummary,
  zebra = false,
  selection,
  columns,
  rows,
  rowKey,
  loading,
  error,
  onRetry,
  emptyText,
  hasMore,
  loadingMore,
  loadMoreError,
  onLoadMore,
  rowActions: providedRowActions,
}: DataTableProps<T>) {
  const { t, locale } = useI18n();
  const [sort, setSort] = useState<{ key: string; direction: 'asc' | 'desc' } | null>(null);
  const sortColumn = columns.find((column) => column.key === sort?.key);
  const visibleRows =
    sort && sortColumn?.sortValue
      ? sortRows(rows, sortColumn.sortValue, sort.direction, locale)
      : rows;
  const selectedCount = rows.filter((row) => selection?.keys.has(rowKey(row))).length;
  const allSelected = rows.length > 0 && selectedCount === rows.length;
  const selectAllRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (selectAllRef.current)
      selectAllRef.current.indeterminate = selectedCount > 0 && !allSelected;
  }, [selectedCount, allSelected]);

  // Navigation actions reuse an already-rendered link; never invent an operation or route.
  const getRowLink = (row: T) => {
    for (const column of columns) {
      const node = column.render(row);
      if (isValidElement<{ to: string }>(node) && node.type === Link) return node.props.to;
    }
    return undefined;
  };
  const rowActions =
    providedRowActions ??
    (rows.some((row) => getRowLink(row))
      ? (row: T): MenuItem[] => {
          const path = getRowLink(row);
          if (!path) return [];
          return [
            { id: 'open', label: t('contextMenu.open'), onSelect: () => navigate(path) },
            {
              id: 'copy-link',
              label: t('contextMenu.copyLink'),
              onSelect: () => {
                const url = new URL(path, window.location.origin).href;
                void copyContextText(url).then((copied) => {
                  if (!copied) window.prompt(t('contextMenu.copyFallback'), url);
                });
              },
            },
          ];
        }
      : undefined);
  const contextMenu = useContextMenu();
  const wrapRef = useRef<HTMLDivElement>(null);
  const overflowing = useHorizontalOverflow(wrapRef, [rows.length, columns.length, loading]);
  const hasShownRows = useRef(false);
  const animateRows = rows.length > 0 && !hasShownRows.current;
  useEffect(() => {
    if (rows.length > 0) hasShownRows.current = true;
  }, [rows.length]);
  if (error) return <ApiErrorAlert error={error} onRetry={onRetry} />;
  return (
    <div className="data-table" aria-busy={loading || undefined}>
      <div className="table-meta" role="status">
        <span>
          {loading
            ? t('state.loading')
            : totalCount !== undefined
              ? t('table.showingTotal', { count: rows.length, total: totalCount })
              : t('table.showingLoaded', { count: rows.length })}
          {filterSummary ? ` · ${t('table.filteredBy', { filters: filterSummary })}` : ''}
        </span>
        {sort || overflowing ? (
          <span className="table-meta-hint">
            {sort ? t('table.sortedLoaded') : t('table.scrollHint')}
          </span>
        ) : null}
      </div>
      {selection && selectedCount > 0 ? (
        <div className="table-bulk">
          <strong>{t('table.selected', { count: selectedCount })}</strong>
          {selection.actions}
          <Button onClick={() => selection.onChange(new Set())}>{t('table.clearSelection')}</Button>
        </div>
      ) : null}
      <div
        ref={wrapRef}
        className={`table-wrap${overflowing ? ' is-overflowing' : ''}`}
        tabIndex={overflowing ? 0 : undefined}
        role="region"
        aria-label={caption}
      >
        <table
          className={`table ${animateRows ? 'table-entrance' : ''} ${zebra ? 'table-zebra' : ''}`}
        >
          <caption className="visually-hidden">{caption}</caption>
          <thead>
            <tr>
              {selection ? (
                <th scope="col" className="table-selection">
                  <input
                    ref={selectAllRef}
                    type="checkbox"
                    aria-label={t('table.selectAll')}
                    checked={allSelected}
                    onChange={() => {
                      const next = new Set(selection.keys);
                      rows.forEach((row) => {
                        if (allSelected) next.delete(rowKey(row));
                        else next.add(rowKey(row));
                      });
                      selection.onChange(next);
                    }}
                  />
                </th>
              ) : null}
              {columns.map((column) => (
                <th
                  key={column.key}
                  scope="col"
                  className={column.className}
                  aria-sort={
                    column.sortValue
                      ? sort?.key === column.key
                        ? sort.direction === 'asc'
                          ? 'ascending'
                          : 'descending'
                        : 'none'
                      : undefined
                  }
                >
                  {column.sortValue ? (
                    <button
                      type="button"
                      className="table-sort"
                      onClick={() =>
                        setSort({
                          key: column.key,
                          direction:
                            sort?.key === column.key && sort.direction === 'asc' ? 'desc' : 'asc',
                        })
                      }
                    >
                      {column.header}
                      <span aria-hidden="true">
                        {sort?.key === column.key ? (sort.direction === 'asc' ? '↑' : '↓') : '↕'}
                      </span>
                    </button>
                  ) : (
                    column.header
                  )}
                </th>
              ))}
              {rowActions ? (
                <th scope="col" className="table-actions-cell">
                  <span className="visually-hidden">{t('contextMenu.actions')}</span>
                </th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {loading
              ? Array.from({ length: 5 }, (_, index) => (
                  <tr key={index} aria-hidden="true">
                    {Array.from(
                      { length: columns.length + (rowActions ? 1 : 0) + (selection ? 1 : 0) },
                      (_, cell) => (
                        <td key={cell}>
                          <span className="table-skeleton" />
                        </td>
                      ),
                    )}
                  </tr>
                ))
              : visibleRows.map((row) => (
                  <tr
                    key={rowKey(row)}
                    data-selected={selection?.keys.has(rowKey(row)) || undefined}
                    {...(rowActions?.(row).length
                      ? { tabIndex: 0, 'data-has-row-actions': true }
                      : {})}
                    onContextMenu={
                      rowActions
                        ? (event) => {
                            const target = event.target;
                            if (
                              !(target instanceof Element) ||
                              target.closest(
                                'input, textarea, select, [contenteditable], a, button',
                              )
                            )
                              return;
                            if (window.getSelection()?.toString().trim()) return;
                            event.preventDefault();
                            contextMenu.openAtPoint(
                              rowActions(row),
                              { x: event.clientX, y: event.clientY },
                              event.currentTarget,
                              t('contextMenu.actions'),
                            );
                          }
                        : undefined
                    }
                    onKeyDown={
                      rowActions
                        ? (event) => {
                            if (
                              (event.key === 'F10' && event.shiftKey) ||
                              event.key === 'ContextMenu'
                            ) {
                              if (
                                event.target instanceof Element &&
                                event.target.closest('input, textarea, select, [contenteditable]')
                              )
                                return;
                              event.preventDefault();
                              contextMenu.openAtElement(
                                rowActions(row),
                                event.currentTarget,
                                t('contextMenu.actions'),
                              );
                            }
                          }
                        : undefined
                    }
                  >
                    {selection ? (
                      <td className="table-selection">
                        <input
                          type="checkbox"
                          aria-label={selection.label(row)}
                          checked={selection.keys.has(rowKey(row))}
                          onChange={() => {
                            const next = new Set(selection.keys);
                            if (next.has(rowKey(row))) next.delete(rowKey(row));
                            else next.add(rowKey(row));
                            selection.onChange(next);
                          }}
                        />
                      </td>
                    ) : null}
                    {columns.map((column) => (
                      <td
                        key={column.key}
                        className={`${column.className ?? ''} ${['ref', 'reference'].includes(column.key) ? 'table-reference' : ''}`}
                      >
                        {column.render(row)}
                      </td>
                    ))}
                    {rowActions ? (
                      <td className="table-actions-cell">
                        {rowActions(row).length > 0 ? (
                          <button
                            type="button"
                            className="table-actions-trigger"
                            aria-label={`${t('contextMenu.actions')}: ${rowKey(row)}`}
                            aria-haspopup="menu"
                            onClick={(event) =>
                              contextMenu.openAtElement(
                                rowActions(row),
                                event.currentTarget,
                                t('contextMenu.actions'),
                              )
                            }
                          >
                            <span aria-hidden="true">⋯</span>
                          </button>
                        ) : null}
                      </td>
                    ) : null}
                  </tr>
                ))}
          </tbody>
        </table>
      </div>
      {!loading && rows.length === 0 ? <EmptyState title={emptyText} /> : null}
      {loadMoreError ? <ApiErrorAlert error={loadMoreError} /> : null}
      {hasMore && onLoadMore ? (
        <div className="load-more">
          <Button onClick={onLoadMore} busy={loadingMore ?? false}>
            {loadingMore ? t('state.loading') : t('action.loadMore')}
          </Button>
        </div>
      ) : null}
      {contextMenu.menu}
    </div>
  );
}
