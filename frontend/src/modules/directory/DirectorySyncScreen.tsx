import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { endpoints } from '../../platform/api/endpoints';
import type { DirectorySyncRequest, DirectorySyncRun, SyncOutcome } from '../../platform/api/types';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime, summarizeSyncCounts } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';

export const outcomeKeys: Record<SyncOutcome, MessageKey> = {
  running: 'sync.outcome.running',
  succeeded: 'sync.outcome.succeeded',
  failed: 'sync.outcome.failed',
  sweep_withheld: 'sync.outcome.sweep_withheld',
};
const outcomeTone = {
  running: 'info',
  succeeded: 'success',
  failed: 'danger',
  sweep_withheld: 'warning',
} as const;

export function OutcomeBadge({ outcome }: { outcome: SyncOutcome }) {
  const { t } = useI18n();
  return <Badge tone={outcomeTone[outcome]}>{t(outcomeKeys[outcome])}</Badge>;
}

export function DirectorySyncScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const list = usePagedList((cursor, signal) => endpoints.directorySyncRuns(cursor, signal), []);
  const [requesting, setRequesting] = useState(false);
  const [requested, setRequested] = useState<DirectorySyncRequest | null>(null);
  const [requestError, setRequestError] = useState<ApiError | undefined>(undefined);

  const requestRun = async () => {
    setRequesting(true);
    setRequested(null);
    setRequestError(undefined);
    try {
      setRequested(await endpoints.requestDirectorySync());
      list.reload();
    } catch (cause) {
      setRequestError(asApiError(cause));
    } finally {
      setRequesting(false);
    }
  };

  const columns: Column<DirectorySyncRun>[] = [
    {
      key: 'started',
      header: t('sync.col.started'),
      render: (run) => (
        <Link to={`/admin/directory-sync/${encodeURIComponent(run.id)}`}>
          {formatDateTime(locale, run.startedAt)}
        </Link>
      ),
    },
    {
      key: 'provider',
      header: t('sync.col.provider'),
      render: (run) => <code>{run.providerKey}</code>,
    },
    {
      key: 'trigger',
      header: t('sync.col.trigger'),
      render: (run) =>
        t(run.trigger === 'manual' ? 'sync.trigger.manual' : 'sync.trigger.scheduled'),
    },
    {
      key: 'outcome',
      header: t('sync.col.outcome'),
      render: (run) => <OutcomeBadge outcome={run.outcome} />,
    },
    {
      key: 'counts',
      header: t('sync.col.counts'),
      render: (run) => {
        const summary = summarizeSyncCounts(run.counts);
        return summary ? t('sync.summary', summary) : '–';
      },
    },
    {
      key: 'conflicts',
      header: t('sync.col.conflicts'),
      className: 'num',
      render: (run) => run.conflictCount,
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.directorySync')}
        intro={t('sync.intro')}
        actions={
          <>
            <Button onClick={list.reload}>{t('action.refresh')}</Button>
            {can('organization.directory.sync') ? (
              <Button variant="primary" busy={requesting} onClick={() => void requestRun()}>
                {t('sync.request')}
              </Button>
            ) : null}
          </>
        }
      />
      {requested ? (
        <Alert kind="info">
          {requested.created
            ? t('sync.request.created', { jobId: requested.jobId })
            : t('sync.request.existing', { jobId: requested.jobId })}
        </Alert>
      ) : null}
      {requestError ? <ApiErrorAlert error={requestError} /> : null}
      <DataTable
        caption={t('nav.directorySync')}
        columns={columns}
        rows={list.items}
        rowKey={(run) => run.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('sync.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
