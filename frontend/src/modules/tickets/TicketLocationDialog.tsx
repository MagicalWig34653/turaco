import { useId, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { organizationApi } from '../organization/api';
import { ticketsApi } from './api';
import type { TicketDetail } from './types';

/** Corrects the location of the affected person (or clears it); the server authorizes and audits. */
export function TicketLocationDialog({
  ticket,
  currentName,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  currentName: string | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const group = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const [picked, setPicked] = useState<{ id: string; name: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const results = useAsync(
    async (signal) =>
      (await organizationApi.searchLocations(debounced, signal)).items.filter((l) => l.active),
    [debounced],
  );
  const save = async (locationId: string | null) => {
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.setLocation(ticket.id, ticket.version, locationId);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (picked && !busy) void save(picked.id);
  };
  return (
    <Dialog title={t('ticketLocation.title')} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <p className="field-hint">{t('ticketLocation.hint')}</p>
        {currentName ? <p>{t('ticketLocation.current', { name: currentName })}</p> : null}
        <TextField
          label={t('ticketLocation.search')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
        <fieldset className="picker-results" aria-busy={results.loading}>
          <legend>{t('ticketLocation.results')}</legend>
          {!results.loading && results.data?.length === 0 ? (
            <p className="empty">{t('ticketLocation.empty')}</p>
          ) : null}
          {(results.data ?? []).map((location) => (
            <label key={location.id} className="picker-option">
              <input
                type="radio"
                name={group}
                checked={picked?.id === location.id}
                onChange={() => setPicked({ id: location.id, name: location.name })}
              />
              <span className="picker-text">{location.name}</span>
            </label>
          ))}
        </fieldset>
        <div className="dialog-actions">
          {ticket.affectedLocationId ? (
            <Button variant="danger" busy={busy} onClick={() => void save(null)}>
              {t('ticketLocation.clear')}
            </Button>
          ) : null}
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!picked}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
