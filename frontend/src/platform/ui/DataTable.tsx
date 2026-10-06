import { useEffect, useRef, type ReactNode } from 'react';
import type { ApiError } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from './Button';
import { ApiErrorAlert } from './ApiErrorAlert';
import { useContextMenu, type MenuItem } from './ContextMenu';

export type Column<T> = {
  key: string;
  header: string;
  render: (row: T) => ReactNode;
  className?: string;
};

type DataTableProps<T> = {
  caption: string;
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
  rowActions,
}: DataTableProps<T>) {
  const { t } = useI18n();
  const contextMenu = useContextMenu();
  const hasShownRows = useRef(false);
  const animateRows = rows.length > 0 && !hasShownRows.current;
  useEffect(() => {
    if (rows.length > 0) hasShownRows.current = true;
  }, [rows.length]);
  if (error) return <ApiErrorAlert error={error} onRetry={onRetry} />;
  if (loading) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  if (rows.length === 0) return <p className="empty">{emptyText}</p>;
  return (
    <div>
      <div className="table-wrap">
        <table className={animateRows ? 'table table-entrance' : 'table'}>
          <caption className="visually-hidden">{caption}</caption>
          <thead>
            <tr>
              {columns.map((column) => (
                <th key={column.key} scope="col" className={column.className}>
                  {column.header}
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
            {rows.map((row) => (
              <tr
                key={rowKey(row)}
                {...(rowActions ? { tabIndex: 0, 'data-has-row-actions': true } : {})}
                onContextMenu={
                  rowActions
                    ? (event) => {
                        const target = event.target;
                        if (
                          !(target instanceof Element) ||
                          target.closest('input, textarea, select, [contenteditable], a, button')
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
                {columns.map((column) => (
                  <td key={column.key} className={column.className}>
                    {column.render(row)}
                  </td>
                ))}
                {rowActions ? (
                  <td className="table-actions-cell">
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
                  </td>
                ) : null}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
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
