import { useId, useState } from 'react';
import { ApiError } from '../../platform/api/client';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { Avatar } from '../../platform/ui/Workspace';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { organizationApi } from './api';
import { lookupState } from './lookupModel';

/**
 * Picks a colleague by name for "on behalf of" forms. It uses the colleague lookup that every
 * signed-in person may call (name and department only, at least three characters). When the
 * lookup is switched off (404) people with directory access fall back to the directory picker.
 */
export function PersonLookup({
  label,
  hint,
  value,
  onChange,
}: {
  label: string;
  hint?: string | undefined;
  value: Assignee | null;
  onChange: (person: Assignee | null) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const name = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 450);
  const [off, setOff] = useState(false);
  const state = lookupState(debounced);
  const results = useAsync(
    async (signal) => {
      if (off || !state.search) return [];
      try {
        return (await organizationApi.lookupPeople(debounced, signal)).items;
      } catch (cause) {
        if (cause instanceof ApiError && cause.status === 404) setOff(true);
        throw cause;
      }
    },
    [debounced, off, state.search],
  );
  if (off && can('organization.view'))
    return (
      <AssigneePicker
        type="user"
        label={label}
        {...(hint ? { hint } : {})}
        value={value}
        onChange={onChange}
      />
    );
  const error = results.error;
  const limited = error?.status === 429;
  return (
    <div className="picker">
      <TextField
        label={label}
        hint={hint}
        type="search"
        value={query}
        maxLength={100}
        autoComplete="off"
        placeholder={t('personLookup.placeholder')}
        disabled={off}
        onChange={(event) => setQuery(event.target.value)}
      />
      {value ? (
        <p className="picker-selected">{t('tasks.assign.selected', { name: value.label })}</p>
      ) : null}
      {off ? <p className="field-hint">{t('personLookup.off')}</p> : null}
      {state.tooShort ? <p className="field-hint">{t('personLookup.tooShort')}</p> : null}
      {limited ? (
        <p className="field-hint" role="status">
          {t('personLookup.rateLimited')}
        </p>
      ) : error && !off ? (
        <p className="field-error" role="status">
          {t('personLookup.unavailable')}
        </p>
      ) : null}
      <fieldset className="picker-results" aria-busy={results.loading}>
        <legend className="visually-hidden">{t('tasks.assign.results')}</legend>
        {state.search && !results.loading && !error && results.data?.length === 0 ? (
          <p className="empty">{t('personLookup.empty')}</p>
        ) : null}
        {(results.data ?? []).map((person) => (
          <label key={person.id} className="picker-option">
            <input
              type="radio"
              name={name}
              checked={value?.id === person.id}
              onChange={() => onChange({ id: person.id, label: person.displayName })}
            />
            <Avatar name={person.displayName} />
            <span className="picker-text">
              <span>{person.displayName}</span>
              {person.department ? (
                <small className="picker-detail">{person.department}</small>
              ) : null}
            </span>
          </label>
        ))}
      </fieldset>
    </div>
  );
}
