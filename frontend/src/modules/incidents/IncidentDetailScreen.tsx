import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextArea } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { incidentsApi } from './api';
import { IncidentBadge } from './IncidentsScreen';

export function IncidentDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => incidentsApi.get(id, signal), [id]);
  const [op, setOp] = useState<string | null>(null);
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const incident = loaded.data;
  if (!incident) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const manage = can('majorincidents.manage');
  const active = incident.status !== 'resolved' && incident.status !== 'closed';
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const postUpdate = (event: FormEvent) => {
    event.preventDefault();
    void run(async () => {
      await incidentsApi.postUpdate(incident.id, message.trim());
      setMessage('');
    });
  };
  return (
    <>
      <PageHeader
        title={`${incident.reference} · ${incident.title}`}
        actions={
          <>
            {active ? (
              <Button
                busy={busy}
                onClick={() =>
                  void run(() => incidentsApi.subscribe(incident.id, !incident.subscribed))
                }
              >
                {t(incident.subscribed ? 'incidents.unfollow' : 'incidents.follow')}
              </Button>
            ) : null}
            {manage
              ? incident.allowedOperations.map((operation) => (
                  <Button
                    key={operation}
                    variant={operation === 'resolve' ? 'primary' : 'secondary'}
                    busy={busy}
                    onClick={() =>
                      operation === 'resolve'
                        ? setOp(operation)
                        : void run(() =>
                            incidentsApi.operate(incident.id, operation, incident.version),
                          )
                    }
                  >
                    {t(`incidents.action.${operation}` as MessageKey)}
                  </Button>
                ))
              : null}
          </>
        }
      />
      <p>
        <Link to="/incidents">{t('incidents.back')}</Link>
      </p>
      {error ? <ApiErrorAlert error={error} /> : null}
      <p>
        <IncidentBadge status={incident.status} /> ·{' '}
        {t('incidents.linked', { count: incident.linkedTickets })}
      </p>
      <p className="preline">{incident.summary}</p>
      <section>
        <h2>{t('incidents.timeline')}</h2>
        <ul className="plain-list">
          {[...incident.updates].reverse().map((u) => (
            <li key={u.id}>
              <strong>{formatDateTime(locale, u.createdAt)}</strong> ·{' '}
              {t(`incidents.status.${u.status as 'identified'}`)}
              <p className="preline">{u.body === '-' ? t('incidents.noMessage') : u.body}</p>
            </li>
          ))}
        </ul>
      </section>
      {manage && incident.status !== 'closed' ? (
        <form className="form" onSubmit={postUpdate}>
          <TextArea
            label={t('incidents.field.update')}
            value={message}
            rows={3}
            maxLength={2000}
            onChange={(event) => setMessage(event.target.value)}
          />
          <div className="form-actions">
            <Button type="submit" busy={busy} disabled={message.trim() === ''}>
              {t('incidents.postUpdate')}
            </Button>
          </div>
        </form>
      ) : null}
      {op === 'resolve' ? (
        <ReasonDialog
          title={t('incidents.action.resolve')}
          label={t('incidents.field.message')}
          hint={t('incidents.resolve.hint')}
          confirmLabel={t('incidents.action.resolve')}
          onClose={() => setOp(null)}
          onSubmit={async (text) => {
            await incidentsApi.operate(incident.id, 'resolve', incident.version, text);
            setOp(null);
            loaded.reload();
          }}
        />
      ) : null}
    </>
  );
}
