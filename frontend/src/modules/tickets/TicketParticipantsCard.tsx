import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { ticketsApi } from './api';
import { participantExpiry } from './participantsModel';

/**
 * Shares the Ticket with external accounts (vendor staff, ADR-0034): they read the Ticket and its public
 * comments and, at the comment level, answer publicly. Shown to people who work the Ticket's Queue.
 */
export function TicketParticipantsCard({ ticketId }: { ticketId: string }) {
  const { t, locale } = useI18n();
  const list = useAsync((signal) => ticketsApi.participants(ticketId, signal), [ticketId]);
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const candidates = useAsync(
    async (signal) =>
      debounced.length >= 2
        ? (await ticketsApi.participantCandidates(ticketId, debounced, signal)).items
        : [],
    [ticketId, debounced],
  );
  const [picked, setPicked] = useState('');
  const [level, setLevel] = useState<'read' | 'comment'>('read');
  const [day, setDay] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [now] = useState(() => new Date());
  const expiry = participantExpiry(day, now);

  const add = async (event: FormEvent) => {
    event.preventDefault();
    if (!picked || busy || (day !== '' && expiry === undefined)) return;
    setBusy(true);
    setError(undefined);
    try {
      await ticketsApi.addParticipant(ticketId, {
        userId: picked,
        level,
        ...(expiry ? { expiresAt: expiry } : {}),
      });
      setPicked('');
      setQuery('');
      setDay('');
      list.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const remove = async (userId: string) => {
    setError(undefined);
    try {
      await ticketsApi.removeParticipant(ticketId, userId);
      list.reload();
    } catch (cause) {
      setError(asApiError(cause));
    }
  };

  return (
    <Card title={t('ticketParticipants.title')}>
      <h2>{t('ticketParticipants.title')}</h2>
      <p className="field-hint">{t('ticketParticipants.intro')}</p>
      {list.error ? (
        <ApiErrorAlert error={list.error} onRetry={list.reload} />
      ) : !list.data ? (
        <Skeleton lines={2} />
      ) : list.data.items.length === 0 ? (
        <p className="empty">{t('ticketParticipants.empty')}</p>
      ) : (
        <ul className="participant-list">
          {list.data.items.map((participant) => (
            <li key={participant.userId}>
              <strong>{participant.displayName || t('ticketWorkspace.unknownPerson')}</strong>{' '}
              <span>{t(`ticketParticipants.level.${participant.level}`)}</span>
              {participant.expiresAt ? (
                <span>
                  {' · '}
                  {t('ticketParticipants.until', {
                    time: formatDateTime(locale, participant.expiresAt),
                  })}
                </span>
              ) : null}{' '}
              <button
                type="button"
                className="btn-link"
                onClick={() => void remove(participant.userId)}
              >
                {t('ticketParticipants.remove')}
              </button>
            </li>
          ))}
        </ul>
      )}
      {error ? <ApiErrorAlert error={error} /> : null}
      <form className="form" onSubmit={(event) => void add(event)}>
        <TextField
          label={t('ticketParticipants.search')}
          hint={t('ticketParticipants.searchHint')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => {
            setQuery(event.target.value);
            setPicked('');
          }}
        />
        {debounced.length >= 2 ? (
          <Select
            label={t('ticketParticipants.person')}
            value={picked}
            onChange={(event) => setPicked(event.target.value)}
            options={[
              { value: '', label: t('ticketParticipants.choose') },
              ...(candidates.data ?? []).map((candidate) => ({
                value: candidate.userId,
                label: `${candidate.displayName} (${candidate.partyName})`,
              })),
            ]}
          />
        ) : null}
        <Select
          label={t('ticketParticipants.level')}
          value={level}
          onChange={(event) => setLevel(event.target.value as 'read' | 'comment')}
          options={[
            { value: 'read', label: t('ticketParticipants.level.read') },
            { value: 'comment', label: t('ticketParticipants.level.comment') },
          ]}
        />
        <TextField
          label={t('ticketParticipants.expires')}
          hint={t('ticketParticipants.expiresHint')}
          type="date"
          value={day}
          onChange={(event) => setDay(event.target.value)}
          error={
            day !== '' && expiry === undefined ? t('ticketParticipants.expiresInvalid') : undefined
          }
        />
        <div className="form-actions">
          <Button type="submit" variant="primary" busy={busy} disabled={!picked}>
            {t('ticketParticipants.add')}
          </Button>
        </div>
      </form>
    </Card>
  );
}
