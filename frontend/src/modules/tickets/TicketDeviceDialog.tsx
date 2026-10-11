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
import { assetsApi } from '../assets/api';
import { ticketsApi } from './api';
import type { TicketDetail } from './types';

/** Corrects the device of the ticket (or clears it); the server authorizes, snapshots and audits. */
export function TicketDeviceDialog({
  ticket,
  onClose,
  onDone,
}: {
  ticket: TicketDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const group = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const [picked, setPicked] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const results = useAsync(
    async (signal) =>
      debounced === '' ? [] : (await assetsApi.list({ q: debounced }, undefined, signal)).items,
    [debounced],
  );
  const save = async (assetId: string | null) => {
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.setDevice(ticket.id, ticket.version, assetId);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (picked && !busy) void save(picked);
  };
  const current = ticket.deviceSnapshot
    ? `${String(ticket.deviceSnapshot.reference ?? '')} ${String(ticket.deviceSnapshot.product ?? '')}`.trim()
    : '';
  return (
    <Dialog title={t('ticketDevice.title')} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <p className="field-hint">{t('ticketDevice.hint')}</p>
        {current ? <p>{t('ticketDevice.current', { name: current })}</p> : null}
        <TextField
          label={t('ticketDevice.search')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
        <fieldset className="picker-results" aria-busy={results.loading}>
          <legend>{t('ticketDevice.results')}</legend>
          {!results.loading && debounced !== '' && results.data?.length === 0 ? (
            <p className="empty">{t('ticketDevice.empty')}</p>
          ) : null}
          {(results.data ?? []).map((asset) => (
            <label key={asset.id} className="picker-option">
              <input
                type="radio"
                name={group}
                checked={picked === asset.id}
                onChange={() => setPicked(asset.id)}
              />
              <span className="picker-text">
                <span>{asset.reference}</span>
                <small className="picker-detail">
                  {[asset.serialNumber, asset.assetTag].filter(Boolean).join(' / ')}
                </small>
              </span>
            </label>
          ))}
        </fieldset>
        <div className="dialog-actions">
          {ticket.assetId ? (
            <Button variant="danger" busy={busy} onClick={() => void save(null)}>
              {t('ticketDevice.clear')}
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
