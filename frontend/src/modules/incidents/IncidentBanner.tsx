import { useState } from 'react';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { incidentsApi } from './api';

/**
 * Known outages shown before a ticket is raised, so people can follow an incident instead of
 * reporting it again. Renders nothing when everything is fine.
 */
export function IncidentBanner() {
  const { t } = useI18n();
  const loaded = useAsync(
    async (signal) => (await incidentsApi.list(true, undefined, signal)).items,
    [],
  );
  const [error, setError] = useState<ApiError | undefined>(undefined);
  if (!loaded.data || loaded.data.length === 0) return null;
  const follow = async (id: string, on: boolean) => {
    setError(undefined);
    try {
      await incidentsApi.subscribe(id, on);
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    }
  };
  return (
    <section aria-label={t('incidents.banner.title')}>
      {error ? <ApiErrorAlert error={error} /> : null}
      {loaded.data.map((m) => (
        <Alert key={m.id} kind="warning" className="incident-banner">
          <span className="incident-banner-icon" aria-hidden="true">
            !
          </span>
          <div>
            <p>
              <strong>
                <Link to={`/incidents/${encodeURIComponent(m.id)}`}>{m.title}</Link>
              </strong>{' '}
              · {t(`incidents.status.${m.status}`)}
            </p>
            <p>{m.summary}</p>
          </div>
          <Button onClick={() => void follow(m.id, !m.subscribed)}>
            {t(m.subscribed ? 'incidents.unfollow' : 'incidents.follow')}
          </Button>
        </Alert>
      ))}
    </section>
  );
}
