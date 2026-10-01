import type { ReactNode } from 'react';
import type { ApiError } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Button } from './Button';
import { ApiErrorAlert } from './ApiErrorAlert';

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
}: DataTableProps<T>) {
  const { t } = useI18n();
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
        <table className="table">
          <caption className="visually-hidden">{caption}</caption>
          <thead>
            <tr>
              {columns.map((column) => (
                <th key={column.key} scope="col" className={column.className}>
                  {column.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={rowKey(row)}>
                {columns.map((column) => (
                  <td key={column.key} className={column.className}>
                    {column.render(row)}
                  </td>
                ))}
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
    </div>
  );
}
