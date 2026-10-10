import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { isoToLocalInput, localInputToIso } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DateTimeField } from '../../platform/ui/DateTimeField';
import { Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { organizationApi } from '../organization/api';
import { PersonPicker, type PickedPerson } from '../organization/PersonPicker';
import { incidentsApi, type MajorIncidentDetail } from './api';

/**
 * Staff-only fields of an incident (majorincidents.manage): owner, promised next update and affected
 * locations. Each is its own explicit, versioned operation.
 */
export function IncidentManagePanel({
  incident,
  onChanged,
}: {
  incident: MajorIncidentDetail;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const active = incident.status !== 'resolved' && incident.status !== 'closed';
  const [owner, setOwner] = useState<PickedPerson | null>(null);
  const [due, setDue] = useState(isoToLocalInput(incident.nextUpdateDue));
  const [query, setQuery] = useState('');
  const search = useDebouncedValue(query.trim(), 250);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const canSearch = can('organization.view');
  const found = useAsync(
    async (signal) =>
      canSearch && search !== ''
        ? (await organizationApi.searchLocations(search, signal)).items.filter(
            (item) => item.active && !incident.locations.some((l) => l.id === item.id),
          )
        : [],
    [search, canSearch, incident.locations.map((l) => l.id).join(',')],
  );
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const ids = incident.locations.map((l) => l.id);
  const dueIso = localInputToIso(due);
  return (
    <section className="link-panel" aria-label={t('incidents.manage.title')}>
      <h2>{t('incidents.manage.title')}</h2>
      {error ? <ApiErrorAlert error={error} /> : null}
      <p>
        {t('incidents.owner')}: <strong>{incident.ownerName ?? t('incidents.owner.none')}</strong>
      </p>
      <PersonPicker label={t('incidents.owner.pick')} value={owner} onChange={setOwner} />
      <div className="form-actions">
        <Button
          busy={busy}
          disabled={!owner}
          onClick={() =>
            void run(async () => {
              if (owner) await incidentsApi.setOwner(incident.id, incident.version, owner.id);
              setOwner(null);
            })
          }
        >
          {t('incidents.owner.set')}
        </Button>
        {incident.ownerUserId ? (
          <Button
            busy={busy}
            onClick={() =>
              void run(() => incidentsApi.setOwner(incident.id, incident.version, null))
            }
          >
            {t('incidents.owner.clear')}
          </Button>
        ) : null}
      </div>
      {active ? (
        <>
          <DateTimeField label={t('incidents.nextUpdate.field')} value={due} onChange={setDue} />
          <div className="form-actions">
            <Button
              busy={busy}
              disabled={!dueIso}
              onClick={() =>
                void run(() =>
                  incidentsApi.setNextUpdate(incident.id, incident.version, dueIso ?? null),
                )
              }
            >
              {t('incidents.nextUpdate.save')}
            </Button>
            {incident.nextUpdateDue ? (
              <Button
                busy={busy}
                onClick={() =>
                  void run(async () => {
                    await incidentsApi.setNextUpdate(incident.id, incident.version, null);
                    setDue('');
                  })
                }
              >
                {t('incidents.nextUpdate.clear')}
              </Button>
            ) : null}
          </div>
        </>
      ) : null}
      <h3>{t('incidents.locations')}</h3>
      {incident.locations.length === 0 ? (
        <p className="empty">{t('incidents.locations.none')}</p>
      ) : (
        <ul className="plain-list">
          {incident.locations.map((l) => (
            <li key={l.id}>
              {l.name}{' '}
              <button
                type="button"
                className="btn-link"
                disabled={busy}
                onClick={() =>
                  void run(() =>
                    incidentsApi.setLocations(
                      incident.id,
                      incident.version,
                      ids.filter((id) => id !== l.id),
                    ),
                  )
                }
              >
                {t('incidents.locations.remove')}
              </button>
            </li>
          ))}
        </ul>
      )}
      {canSearch ? (
        <>
          <TextField
            label={t('incidents.locations.search')}
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            maxLength={100}
          />
          {found.error ? <ApiErrorAlert error={found.error} onRetry={found.reload} /> : null}
          {(found.data ?? []).length > 0 ? (
            <Select
              label={t('incidents.locations.pick')}
              value=""
              onChange={(event) => {
                const id = event.target.value;
                if (!id) return;
                setQuery('');
                void run(() =>
                  incidentsApi.setLocations(incident.id, incident.version, [...ids, id]),
                );
              }}
              options={[
                { value: '', label: '-' },
                ...(found.data ?? []).map((l) => ({ value: l.id, label: l.name })),
              ]}
            />
          ) : null}
        </>
      ) : null}
    </section>
  );
}
