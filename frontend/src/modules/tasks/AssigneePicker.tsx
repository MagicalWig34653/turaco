import { useId, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { TextField } from '../../platform/ui/Field';
import { Avatar } from '../../platform/ui/Workspace';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { organizationApi } from '../organization/api';

export type AssigneeType = 'user' | 'team';
export type Assignee = { id: string; label: string };

type Props = {
  type: AssigneeType;
  value: Assignee | null;
  onChange: (assignee: Assignee | null) => void;
  /** Overrides the search field label, e.g. "Owner". */
  label?: string;
  hint?: string;
};

/** Search picker for active Users and Teams (/users?q=, /teams?q=); needs organization.view. */
export function AssigneePicker({ type, value, onChange, label, hint }: Props) {
  const { t } = useI18n();
  const { can } = useSession();
  const name = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const allowed = can('organization.view');

  const results = useAsync(
    async (signal): Promise<Array<Assignee & { detail: string | undefined }>> => {
      if (!allowed) return [];
      if (type === 'user') {
        const page = await organizationApi.searchUsers(debounced, signal);
        return page.items
          .filter((user) => user.status === 'active')
          .map((user) => ({
            id: user.id,
            label: user.displayName,
            detail: user.primaryEmail ?? undefined,
          }));
      }
      const page = await organizationApi.searchTeams(debounced, signal);
      return page.items
        .filter((team) => team.active)
        .map((team) => ({ id: team.id, label: team.name, detail: undefined }));
    },
    [type, debounced, allowed],
  );

  if (!allowed) return <p className="field-hint">{t('tasks.assign.noPermission')}</p>;

  return (
    <div className="picker">
      <TextField
        label={
          label ?? (type === 'user' ? t('tasks.assign.search.user') : t('tasks.assign.search.team'))
        }
        hint={hint}
        placeholder={t('tasks.assign.searchPlaceholder')}
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        maxLength={100}
        autoComplete="off"
      />
      {value ? (
        <p className="picker-selected">{t('tasks.assign.selected', { name: value.label })}</p>
      ) : null}
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      <fieldset className="picker-results" aria-busy={results.loading}>
        <legend className="visually-hidden">{t('tasks.assign.results')}</legend>
        {results.loading ? <p role="status">{t('state.loading')}</p> : null}
        {!results.loading && results.data && results.data.length === 0 ? (
          <p className="empty">{t('tasks.assign.noResults')}</p>
        ) : null}
        {(results.data ?? []).map((candidate) => (
          <label key={candidate.id} className="picker-option">
            <input
              type="radio"
              name={name}
              checked={value?.id === candidate.id}
              onChange={() => onChange({ id: candidate.id, label: candidate.label })}
            />
            <Avatar name={candidate.label} />
            <span className="picker-text">
              <span>{candidate.label}</span>
              {candidate.detail ? (
                <small className="picker-detail">{candidate.detail}</small>
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
