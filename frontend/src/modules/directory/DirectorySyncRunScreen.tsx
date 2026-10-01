import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { OutcomeBadge } from './DirectorySyncScreen';
import { directoryApi } from './api';
import type { SyncConflict, SyncConflictKind } from './types';

const conflictKeys: Record<SyncConflictKind, MessageKey> = {
  email_in_use: 'sync.conflict.email_in_use',
  manager_unresolved: 'sync.conflict.manager_unresolved',
  invalid_attributes: 'sync.conflict.invalid_attributes',
};

export function DirectorySyncRunScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const run = useAsync((signal) => directoryApi.syncRun(id, signal), [id]);
  const data = run.data;

  const conflictColumns: Column<SyncConflict>[] = [
    { key: 'kind', header: t('sync.conflict.kind'), render: (c) => t(conflictKeys[c.kind]) },
    {
      key: 'externalId',
      header: t('sync.conflict.externalId'),
      render: (c) => <code>{c.externalId}</code>,
    },
    { key: 'username', header: t('sync.conflict.username'), render: (c) => c.username },
  ];
  const counts = Object.entries(data?.counts ?? {}).sort(([a], [b]) => a.localeCompare(b));

  return (
    <>
      <PageHeader
        title={t('sync.detail.title')}
        actions={
          <Link to="/admin/directory-sync" className="btn btn-secondary">
            {t('sync.back')}
          </Link>
        }
      />
      {run.loading ? <p role="status">{t('state.loading')}</p> : null}
      {run.error ? <ApiErrorAlert error={run.error} onRetry={run.reload} /> : null}
      {data ? (
        <>
          {data.error ? (
            <Alert kind="error">
              <strong>{t('sync.error')}</strong>
              <p>{data.error}</p>
            </Alert>
          ) : null}
          <section className="card-plain" aria-labelledby="run-facts">
            <h2 id="run-facts">{t('sync.section.overview')}</h2>
            <dl className="facts">
              <dt>{t('sync.col.outcome')}</dt>
              <dd>
                <OutcomeBadge outcome={data.outcome} />
              </dd>
              <dt>{t('sync.col.provider')}</dt>
              <dd>
                <code>{data.providerKey}</code>
              </dd>
              <dt>{t('sync.col.trigger')}</dt>
              <dd>
                {t(data.trigger === 'manual' ? 'sync.trigger.manual' : 'sync.trigger.scheduled')}
              </dd>
              <dt>{t('sync.col.started')}</dt>
              <dd>{formatDateTime(locale, data.startedAt)}</dd>
              <dt>{t('sync.observedAt')}</dt>
              <dd>{formatDateTime(locale, data.observedAt)}</dd>
              <dt>{t('sync.finishedAt')}</dt>
              <dd>{formatDateTime(locale, data.finishedAt)}</dd>
              <dt>{t('sync.jobId')}</dt>
              <dd>{data.jobId ? <code>{data.jobId}</code> : '–'}</dd>
              <dt>{t('sync.runId')}</dt>
              <dd>
                <code>{data.id}</code>
              </dd>
            </dl>
          </section>
          <section className="card-plain" aria-labelledby="run-counts">
            <h2 id="run-counts">{t('sync.section.counts')}</h2>
            {counts.length === 0 ? (
              <p className="empty">{t('sync.counts.none')}</p>
            ) : (
              <dl className="facts facts-compact">
                {counts.map(([name, value]) => (
                  <div key={name} className="fact-row">
                    <dt>
                      <code>{name}</code>
                    </dt>
                    <dd>{value}</dd>
                  </div>
                ))}
              </dl>
            )}
          </section>
          <section className="card-plain" aria-labelledby="run-conflicts">
            <h2 id="run-conflicts">{t('sync.section.conflicts', { count: data.conflictCount })}</h2>
            {data.conflictCount > data.conflicts.length ? (
              <p className="field-hint">
                {t('sync.conflicts.truncated', {
                  shown: data.conflicts.length,
                  total: data.conflictCount,
                })}
              </p>
            ) : null}
            <DataTable
              caption={t('sync.section.conflicts', { count: data.conflictCount })}
              columns={conflictColumns}
              rows={data.conflicts}
              rowKey={(c) => `${c.kind}:${c.externalId}`}
              emptyText={t('sync.conflicts.none')}
            />
          </section>
        </>
      ) : null}
    </>
  );
}
