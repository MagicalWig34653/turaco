import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { organizationApi } from '../organization/api';
import { lookupState } from '../organization/lookupModel';
import { addMention, maxMentions, removeMention, type Mentioned } from './ticketExtrasModel';

/**
 * Picks colleagues to notify from an internal note. They are found with the colleague lookup, so
 * no directory permission is needed; the server decides who may actually be notified.
 */
export function MentionPicker({
  value,
  onChange,
  disabled = false,
}: {
  value: readonly Mentioned[];
  onChange: (next: Mentioned[]) => void;
  disabled?: boolean;
}) {
  const { t } = useI18n();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const state = lookupState(debounced);
  const results = useAsync(
    async (signal) =>
      state.search ? (await organizationApi.lookupPeople(debounced, signal)).items : [],
    [debounced, state.search],
  );
  const full = value.length >= maxMentions;
  return (
    <div className="mention-picker">
      <TextField
        label={t('ticketMention.label')}
        hint={t('ticketMention.hint')}
        type="search"
        value={query}
        disabled={disabled || full}
        maxLength={100}
        autoComplete="off"
        onChange={(event) => setQuery(event.target.value)}
      />
      {full ? <p className="field-hint">{t('ticketMention.limit', { max: maxMentions })}</p> : null}
      {state.tooShort ? <p className="field-hint">{t('personLookup.tooShort')}</p> : null}
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      {state.search && !results.loading && !results.error && results.data?.length === 0 ? (
        <p className="empty">{t('people.picker.empty')}</p>
      ) : null}
      {!full && (results.data ?? []).length > 0 ? (
        <ul className="plain-list" aria-label={t('people.picker.results')}>
          {(results.data ?? [])
            .filter((person) => !value.some((entry) => entry.id === person.id))
            .map((person) => (
              <li key={person.id}>
                <button
                  type="button"
                  className="btn-link"
                  disabled={disabled}
                  onClick={() => {
                    onChange(addMention(value, { id: person.id, label: person.displayName }));
                    setQuery('');
                  }}
                >
                  {person.displayName}
                  {person.department ? ` (${person.department})` : ''}
                </button>
              </li>
            ))}
        </ul>
      ) : null}
      {value.length > 0 ? (
        <ul className="plain-list mention-chips" aria-label={t('ticketMention.selected')}>
          {value.map((person) => (
            <li key={person.id}>
              <button
                type="button"
                className="filter-chip"
                disabled={disabled}
                aria-label={t('ticketMention.remove', { name: person.label })}
                onClick={() => onChange(removeMention(value, person.id))}
              >
                @{person.label} <span aria-hidden="true">×</span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <p className="field-hint">{t('ticketMention.notified')}</p>
    </div>
  );
}
