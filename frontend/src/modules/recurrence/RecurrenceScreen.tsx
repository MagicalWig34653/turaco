import { TableDate } from '../../platform/ui/TableDate';
import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { recurrenceApi } from './api';
import { describeRule } from './describe';
import type { RecurringTaskDefinition } from './types';

export function RecurrenceScreen() {
  const { t } = useI18n();
  const list = usePagedList((cursor, signal) => recurrenceApi.list(cursor, signal), []);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [toDelete, setToDelete] = useState<RecurringTaskDefinition | null>(null);

  const run = async (definition: RecurringTaskDefinition, action: () => Promise<unknown>) => {
    setBusyId(definition.id);
    setError(undefined);
    try {
      await action();
      setToDelete(null);
      list.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusyId(null);
    }
  };

  const columns: Column<RecurringTaskDefinition>[] = [
    {
      key: 'title',
      header: t('tasks.col.title'),
      render: (d) => (
        <Link to={`/admin/recurring-tasks/${encodeURIComponent(d.id)}`}>{d.title}</Link>
      ),
    },
    { key: 'rule', header: t('recurrence.col.schedule'), render: (d) => describeRule(t, d.rule) },
    {
      key: 'state',
      header: t('tasks.col.status'),
      render: (d) =>
        d.active ? (
          <Badge tone="success">{t('recurrence.state.active')}</Badge>
        ) : (
          <Badge tone="warning">{t('recurrence.state.paused')}</Badge>
        ),
    },
    {
      key: 'next',
      header: t('recurrence.col.nextRun'),
      render: (d) => <TableDate value={d.nextRunAt} />,
    },
    {
      key: 'actions',
      header: t('notifications.col.action'),
      render: (d) => (
        <>
          <Button
            disabled={busyId !== null}
            onClick={() =>
              void run(d, () =>
                d.active
                  ? recurrenceApi.pause(d.id, d.version)
                  : recurrenceApi.resume(d.id, d.version),
              )
            }
          >
            {d.active ? t('recurrence.action.pause') : t('recurrence.action.resume')}
          </Button>{' '}
          <Button variant="danger" disabled={busyId !== null} onClick={() => setToDelete(d)}>
            {t('recurrence.action.delete')}
          </Button>
        </>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.recurrence')}
        intro={t('recurrence.intro')}
        actions={
          <Link to="/admin/recurring-tasks/new" className="btn btn-primary">
            {t('recurrence.create.action')}
          </Link>
        }
      />
      {error ? <ApiErrorAlert error={error} onRetry={list.reload} /> : null}
      <DataTable
        caption={t('nav.recurrence')}
        columns={columns}
        rows={list.items}
        rowKey={(d) => d.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('recurrence.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {toDelete ? (
        <ConfirmDialog
          title={t('recurrence.delete.title')}
          message={t('recurrence.delete.message', { title: toDelete.title })}
          confirmLabel={t('recurrence.action.delete')}
          danger
          busy={busyId === toDelete.id}
          onConfirm={() =>
            void run(toDelete, () => recurrenceApi.remove(toDelete.id, toDelete.version))
          }
          onCancel={() => setToDelete(null)}
        />
      ) : null}
    </>
  );
}
