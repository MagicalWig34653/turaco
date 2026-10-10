import { useId, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { Avatar } from '../../platform/ui/Workspace';
import { organizationApi } from '../organization/api';
import type { SubjectType } from './types';

export type Subject = { id: string; label: string };

type Candidate = { id: string; label: string; detail: string | undefined; disabled: boolean };

type Props = {
  subjectType: SubjectType;
  value: Subject | null;
  onChange: (subject: Subject | null) => void;
};

/** Search picker for Users (/users?q=) and Directory Groups (/directory-groups?q=). */
export function SubjectPicker({ subjectType, value, onChange }: Props) {
  const { t } = useI18n();
  const { can } = useSession();
  const name = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const permission = subjectType === 'user' ? 'organization.view' : 'organization.directory.view';
  const allowed = can(permission);

  const results = useAsync(
    async (signal): Promise<Candidate[]> => {
      if (!allowed) return [];
      if (subjectType === 'user') {
        const page = await organizationApi.searchUsers(debounced, signal);
        return page.items.map((user) => ({
          id: user.id,
          label: user.displayName,
          detail: user.primaryEmail ?? undefined,
          disabled: false,
        }));
      }
      const page = await organizationApi.searchDirectoryGroups(debounced, signal);
      return page.items.map((group) => ({
        id: group.id,
        label: group.displayName,
        detail: group.providerKey,
        disabled: Boolean(group.deletedObservedAt),
      }));
    },
    [subjectType, debounced, allowed],
  );

  if (!allowed) {
    return <p className="field-hint">{t('assign.picker.noPermission', { permission })}</p>;
  }

  return (
    <div className="picker">
      <TextField
        label={subjectType === 'user' ? t('assign.search.user') : t('assign.search.group')}
        hint={t('assign.search.hint')}
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        maxLength={100}
        autoComplete="off"
      />
      {value ? (
        <p className="picker-selected">{t('assign.selected', { name: value.label })}</p>
      ) : null}
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      <fieldset className="picker-results" aria-busy={results.loading}>
        <legend>{t('assign.results')}</legend>
        {results.loading ? <p role="status">{t('state.loading')}</p> : null}
        {!results.loading && results.data && results.data.length === 0 ? (
          <p className="empty">{t('assign.noResults')}</p>
        ) : null}
        {(results.data ?? []).map((candidate) => (
          <label key={candidate.id} className="picker-option">
            <input
              type="radio"
              name={name}
              checked={value?.id === candidate.id}
              disabled={candidate.disabled}
              onChange={() => onChange({ id: candidate.id, label: candidate.label })}
            />
            <Avatar name={candidate.label} />
            <span className="picker-text">
              <span>{candidate.label}</span>
              {candidate.detail ? (
                <small className="picker-detail">{candidate.detail}</small>
              ) : null}
              {candidate.disabled ? (
                <small className="picker-detail">{t('assign.group.deleted')}</small>
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
