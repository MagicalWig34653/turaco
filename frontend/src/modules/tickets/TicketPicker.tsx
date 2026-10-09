import { useId, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { Button } from '../../platform/ui/Button';
import { TextField } from '../../platform/ui/Field';
import { lookupTickets, planLookup, type TicketHit } from './ticketLookup';

/**
 * Search-and-pick list for Tickets: type a reference (also an earlier number), part of the title
 * or a few words. The server scopes the results to what the caller may see. Used wherever a Ticket
 * is linked, so nobody has to paste an id.
 */
export function TicketPicker({
  label,
  hint,
  selected,
  multiple = false,
  excludeIds = [],
  disabled = false,
  onChange,
}: {
  label: string;
  hint?: string | undefined;
  selected: readonly TicketHit[];
  multiple?: boolean;
  excludeIds?: readonly string[];
  disabled?: boolean;
  onChange: (selected: TicketHit[]) => void;
}) {
  const { t } = useI18n();
  const listId = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const plan = planLookup(debounced);
  const results = useAsync(
    async (signal) => (plan.reference || plan.text ? lookupTickets(debounced, signal) : undefined),
    [debounced],
  );
  const hits = (results.data?.hits ?? []).filter((hit) => !excludeIds.includes(hit.id));
  const isSelected = (id: string) => selected.some((hit) => hit.id === id);
  const toggle = (hit: TicketHit) => {
    if (isSelected(hit.id)) onChange(selected.filter((item) => item.id !== hit.id));
    else onChange(multiple ? [...selected, hit] : [hit]);
  };
  const waiting = query.trim() !== debounced || results.loading;
  return (
    <div className="ticket-picker">
      <TextField
        label={label}
        hint={hint ?? t('ticketPicker.hint')}
        type="search"
        value={query}
        maxLength={100}
        autoComplete="off"
        disabled={disabled}
        aria-controls={listId}
        placeholder={t('ticketPicker.placeholder')}
        onChange={(event) => setQuery(event.target.value)}
      />
      {selected.length > 0 ? (
        <ul className="ticket-picker-selected" aria-label={t('ticketPicker.selected')}>
          {selected.map((hit) => (
            <li key={hit.id}>
              <span className="ticket-picker-chip">
                <strong>{hit.reference}</strong>
                {hit.title ? <span>{hit.title}</span> : null}
                <button
                  type="button"
                  className="ticket-picker-remove"
                  disabled={disabled}
                  aria-label={t('ticketPicker.remove', { reference: hit.reference })}
                  onClick={() => onChange(selected.filter((item) => item.id !== hit.id))}
                >
                  <span aria-hidden="true">×</span>
                </button>
              </span>
            </li>
          ))}
        </ul>
      ) : null}
      <div id={listId} className="ticket-picker-results" aria-busy={waiting}>
        {plan.tooShort ? (
          <p className="field-hint" role="status">
            {t('ticketPicker.tooShort')}
          </p>
        ) : null}
        {waiting && (plan.reference || plan.text) ? (
          <p className="field-hint" role="status">
            {t('state.loading')}
          </p>
        ) : null}
        {!waiting && results.error ? (
          <p className="field-error" role="status">
            {t('ticketPicker.unavailable')}{' '}
            <Button onClick={results.reload}>{t('action.retry')}</Button>
          </p>
        ) : null}
        {!waiting && results.data?.unavailable ? (
          <p className="field-error" role="status">
            {t('ticketPicker.unavailable')}
          </p>
        ) : null}
        {!waiting && results.data && hits.length === 0 && !results.data.unavailable ? (
          <p className="empty">{t('ticketPicker.empty')}</p>
        ) : null}
        <ul>
          {hits.map((hit) => (
            <li key={hit.id}>
              <button
                type="button"
                className="ticket-picker-option"
                aria-pressed={isSelected(hit.id)}
                disabled={disabled}
                onClick={() => toggle(hit)}
              >
                <strong>{hit.reference}</strong>
                <span>{hit.title}</span>
                {hit.alias ? <small>{t('ticketPicker.alias')}</small> : null}
              </button>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}
