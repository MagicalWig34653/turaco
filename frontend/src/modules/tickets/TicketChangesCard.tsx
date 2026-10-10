import { useId, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { TextField } from '../../platform/ui/Field';
import { Card, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { changesApi } from '../changes/api';
import { ticketsApi } from './api';
import { changeCandidates } from './ticketExtrasModel';
import type { TicketChangeLink } from './types';

function LinkChangeDialog({
  ticketId,
  linked,
  onClose,
  onDone,
}: {
  ticketId: string;
  linked: readonly TicketChangeLink[];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const group = useId();
  const [query, setQuery] = useState('');
  const [picked, setPicked] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const changes = useAsync(
    async (signal) => (await changesApi.list({}, undefined, signal)).items,
    [],
  );
  const options = changeCandidates(
    changes.data ?? [],
    query,
    linked.map((link) => link.changeId),
  );
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!picked || busy) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.linkChange(ticketId, picked);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('ticketChanges.link.title')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('ticketChanges.link.search')}
          hint={t('ticketChanges.link.hint')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        {changes.error ? <ApiErrorAlert error={changes.error} onRetry={changes.reload} /> : null}
        <fieldset className="picker-results" aria-busy={changes.loading}>
          <legend>{t('ticketChanges.link.results')}</legend>
          {changes.loading ? <Skeleton lines={2} /> : null}
          {!changes.loading && options.length === 0 ? (
            <p className="empty">{t('ticketChanges.link.empty')}</p>
          ) : null}
          {options.map((change) => (
            <label key={change.id} className="picker-option">
              <input
                type="radio"
                name={group}
                checked={picked === change.id}
                onChange={() => setPicked(change.id)}
              />
              <span className="picker-text">
                <span>
                  {change.reference} {change.title}
                </span>
                <small className="picker-detail">
                  {t(`changes.status.${change.status}` as MessageKey)}
                </small>
              </span>
            </label>
          ))}
        </fieldset>
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!picked}>
            {t('ticketChanges.link.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

/** "Linked changes" of a Ticket: the list for staff, link and unlink for those who may edit. */
export function TicketChangesCard({
  ticketId,
  canEdit,
  onChanged,
}: {
  ticketId: string;
  canEdit: boolean;
  onChanged?: () => void;
}) {
  const { t } = useI18n();
  const links = useAsync(
    async (signal) => (await ticketsApi.changes(ticketId, signal)).items,
    [ticketId],
  );
  const [dialog, setDialog] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [removing, setRemoving] = useState<string | null>(null);
  const items = links.data ?? [];
  const unlink = async (changeId: string) => {
    setRemoving(changeId);
    setError(undefined);
    try {
      await ticketsApi.unlinkChange(ticketId, changeId);
      links.reload();
      onChanged?.();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setRemoving(null);
    }
  };
  // A caller without access to Changes gets a 403; the card then simply is not offered.
  if (links.error && links.error.status === 403) return null;
  return (
    <Card title={t('ticketChanges.title')}>
      <h2>{t('ticketChanges.title')}</h2>
      {links.error ? <ApiErrorAlert error={links.error} onRetry={links.reload} /> : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {links.loading && !links.data ? <Skeleton lines={2} /> : null}
      {links.data && items.length === 0 ? <p className="empty">{t('ticketChanges.none')}</p> : null}
      <ul className="plain-list">
        {items.map((link) => (
          <li key={link.id}>
            {link.hidden ? (
              <span>{t('ticketChanges.hidden')}</span>
            ) : (
              <>
                <Link to={`/changes/${encodeURIComponent(link.changeId)}`}>
                  {link.reference} {link.title}
                </Link>{' '}
                {link.status ? (
                  <StatusBadge tone="neutral">
                    {t(`changes.status.${link.status}` as MessageKey)}
                  </StatusBadge>
                ) : null}
              </>
            )}{' '}
            {canEdit ? (
              <Button busy={removing === link.changeId} onClick={() => void unlink(link.changeId)}>
                {t('ticketChanges.unlink')}
              </Button>
            ) : null}
          </li>
        ))}
      </ul>
      {canEdit ? <Button onClick={() => setDialog(true)}>{t('ticketChanges.link')}</Button> : null}
      {dialog ? (
        <LinkChangeDialog
          ticketId={ticketId}
          linked={items}
          onClose={() => setDialog(false)}
          onDone={() => {
            setDialog(false);
            links.reload();
            onChanged?.();
          }}
        />
      ) : null}
    </Card>
  );
}
