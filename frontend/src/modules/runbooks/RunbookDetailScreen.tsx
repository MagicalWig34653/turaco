import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate, useLocation } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { runbooksApi, type Execution } from './api';

export function RunbookDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const { search } = useLocation();
  const ticketId = new URLSearchParams(search).get('ticket') ?? '';
  const loaded = useAsync((signal) => runbooksApi.get(id, signal), [id]);
  const runs = useAsync(
    (signal) => runbooksApi.executions({ runbookId: id }, undefined, signal),
    [id],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [cancelling, setCancelling] = useState<Execution | null>(null);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const runbook = loaded.data;
  if (!runbook) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const manage = can('knowledge.manage');
  const execute = can('runbooks.execute');
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      loaded.reload();
      runs.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <PageHeader
        title={`${runbook.reference} · ${runbook.title}`}
        actions={
          <>
            {execute && runbook.active ? (
              <Button
                variant="primary"
                busy={busy}
                onClick={() =>
                  void run(async () => {
                    await runbooksApi.start(runbook.id, ticketId || undefined);
                  })
                }
              >
                {t(ticketId ? 'runbooks.startForTicket' : 'runbooks.start')}
              </Button>
            ) : null}
            {manage ? (
              <>
                <Link
                  to={`/runbooks/${encodeURIComponent(runbook.id)}/edit`}
                  className="btn btn-secondary"
                >
                  {t('runbooks.edit')}
                </Link>
                <Button
                  busy={busy}
                  onClick={() =>
                    void run(() =>
                      runbooksApi.setActive(runbook.id, !runbook.active, runbook.version),
                    )
                  }
                >
                  {t(runbook.active ? 'runbooks.deactivate' : 'runbooks.activate')}
                </Button>
              </>
            ) : null}
          </>
        }
      />
      <p>
        <Link to="/runbooks">{t('runbooks.back')}</Link>
        {ticketId ? (
          <>
            {' · '}
            <Link to={`/support/${encodeURIComponent(ticketId)}`}>
              {t('runbooks.backToTicket')}
            </Link>
          </>
        ) : null}
      </p>
      {error ? <ApiErrorAlert error={error} /> : null}
      <p>
        <Badge tone={runbook.active ? 'success' : 'neutral'}>
          {t(runbook.active ? 'runbooks.active' : 'runbooks.inactive')}
        </Badge>
      </p>
      {runbook.description ? <p className="preline">{runbook.description}</p> : null}
      <ol>
        {runbook.steps.map((s, i) => (
          <li key={i}>
            <strong>{s.title}</strong>
            {s.description ? <p className="preline">{s.description}</p> : null}
          </li>
        ))}
      </ol>
      <section>
        <h2>{t('runbooks.executions')}</h2>
        {runs.error ? <ApiErrorAlert error={runs.error} onRetry={runs.reload} /> : null}
        {runs.data && runs.data.items.length === 0 ? (
          <p className="empty">{t('runbooks.executions.none')}</p>
        ) : null}
        <ul className="plain-list">
          {(runs.data?.items ?? []).map((e) => (
            <li key={e.id}>
              {formatDateTime(locale, e.createdAt)} ·{' '}
              <Badge
                tone={
                  e.status === 'completed'
                    ? 'success'
                    : e.status === 'running'
                      ? 'warning'
                      : 'neutral'
                }
              >
                {t(`runbooks.status.${e.status}`)}
              </Badge>{' '}
              {e.status === 'running' ? (
                <>
                  <Button onClick={() => navigate('/my-work')}>{t('runbooks.openTasks')}</Button>{' '}
                  {execute ? (
                    <Button onClick={() => setCancelling(e)}>{t('runbooks.cancel')}</Button>
                  ) : null}
                </>
              ) : null}
            </li>
          ))}
        </ul>
      </section>
      {cancelling ? (
        <ReasonDialog
          title={t('runbooks.cancel')}
          label={t('runbooks.cancel.reason')}
          confirmLabel={t('runbooks.cancel')}
          danger
          onClose={() => setCancelling(null)}
          onSubmit={async (reason) => {
            await runbooksApi.cancel(cancelling.id, reason);
            setCancelling(null);
            runs.reload();
          }}
        />
      ) : null}
    </>
  );
}
