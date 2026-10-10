import { useId, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { Avatar } from '../../platform/ui/Workspace';
import { peopleAdminApi } from './adminApi';
import type { PersonRow } from './adminTypes';

export type PickedPerson = { id: string; label: string };

/** Search-and-pick for one active person (by name or e-mail); needs the permission to view the directory. */
export function PersonPicker({
  label,
  value,
  onChange,
  exclude = [],
}: {
  label?: string;
  value: PickedPerson | null;
  onChange: (person: PickedPerson | null) => void;
  exclude?: readonly string[];
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const name = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const allowed = can('organization.view');
  const results = useAsync(
    async (signal): Promise<PersonRow[]> => {
      if (!allowed) return [];
      const page = await peopleAdminApi.searchUsers(debounced, signal);
      return page.items.filter(
        (person) => person.status === 'active' && !exclude.includes(person.id),
      );
    },
    [debounced, allowed, exclude.join(',')],
  );
  if (!allowed) return <p className="field-hint">{t('people.picker.noPermission')}</p>;
  return (
    <div className="picker">
      <TextField
        label={label ?? t('people.picker.search')}
        hint={t('people.picker.hint')}
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        maxLength={100}
        autoComplete="off"
      />
      {value ? (
        <p className="picker-selected">
          {t('people.picker.selected', { name: value.label })}{' '}
          <button type="button" className="btn-link" onClick={() => onChange(null)}>
            {t('people.picker.clear')}
          </button>
        </p>
      ) : null}
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      <fieldset className="picker-results" aria-busy={results.loading}>
        <legend>{t('people.picker.results')}</legend>
        {results.loading ? <p role="status">{t('state.loading')}</p> : null}
        {!results.loading && results.data?.length === 0 ? (
          <p className="empty">{t('people.picker.empty')}</p>
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
              {person.primaryEmail ? (
                <small className="picker-detail">{person.primaryEmail}</small>
              ) : null}
            </span>
            <span className="picker-check" aria-hidden="true">
              ✓
            </span>
          </label>
        ))}
      </fieldset>
    </div>
  );
}
