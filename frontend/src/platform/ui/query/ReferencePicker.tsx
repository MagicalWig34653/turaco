import { useId, useState } from 'react';
import { api } from '../../api/client';
import { useAsync } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import { useSession } from '../../session/SessionProvider';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { useDebouncedValue } from '../hooks';
import {
  referenceKind,
  referencePath,
  rememberReference,
  useReferenceNames,
  type ReferenceKind,
} from './referenceNames';

type Item = {
  id: string;
  displayName?: string;
  name?: string;
  reference?: string;
  status?: string;
  active?: boolean;
};

const label = (item: Item) => item.displayName ?? item.name ?? item.reference ?? item.id;

/** Permission needed to search each reference list; the server still authorizes every call. */
function useCanSearch(kind: ReferenceKind | undefined): boolean {
  const { can } = useSession();
  if (kind === 'users' || kind === 'teams' || kind === 'departments' || kind === 'locations')
    return can('organization.view');
  if (kind === 'assets') return can('assets.view') || can('assets.manage');
  // The Queue list is limited by the server to the Queues the caller may know.
  return kind === 'queues';
}

/** Search-and-pick list for user, team and asset references; supports one or many selections. */
export function ReferencePicker({
  resource,
  value,
  multiple,
  onChange,
}: {
  resource: string | undefined;
  value: string[];
  multiple: boolean;
  onChange: (value: string[]) => void;
}) {
  const { t } = useI18n();
  const kind = referenceKind(resource);
  const allowed = useCanSearch(kind);
  const name = useId();
  const [search, setSearch] = useState('');
  const debounced = useDebouncedValue(search.trim(), 250);
  const results = useAsync(
    async (signal) => {
      if (!kind || !allowed) return [];
      const page = await api.get<{ items: Item[] }>(referencePath(kind), {
        signal,
        query: kind === 'queues' ? {} : { q: debounced, limit: 25 },
      });
      const needle = debounced.toLocaleLowerCase();
      return page.items.filter(
        (item) =>
          item.active !== false &&
          item.status !== 'inactive' &&
          item.status !== 'archived' &&
          // The Queue list has no server-side search.
          (kind !== 'queues' || !needle || label(item).toLocaleLowerCase().includes(needle)),
      );
    },
    [kind, allowed, debounced],
  );
  const resolve = useReferenceNames(kind ? value.map((id) => ({ kind, id })) : []);
  if (!kind) return null;
  if (!allowed) return <p className="field-hint">{t('query.referenceNoPermission')}</p>;
  const toggle = (item: Item) => {
    rememberReference(kind, item.id, label(item));
    if (!multiple) onChange([item.id]);
    else
      onChange(
        value.includes(item.id) ? value.filter((id) => id !== item.id) : [...value, item.id],
      );
  };
  return (
    <div className="query-reference">
      <label>
        <span>{t('query.referenceSearch')}</span>
        <input
          type="search"
          value={search}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setSearch(event.target.value)}
        />
      </label>
      {value.length > 0 ? (
        <p className="query-reference-selected">
          {value.map((id) => resolve(kind, id) ?? t('query.referenceUnknown')).join(', ')}
        </p>
      ) : null}
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      <fieldset className="query-reference-results" aria-busy={results.loading}>
        <legend className="visually-hidden">{t('query.referenceResults')}</legend>
        {results.loading ? <p role="status">{t('state.loading')}</p> : null}
        {!results.loading && results.data?.length === 0 ? (
          <p className="empty">{t('query.referenceEmpty')}</p>
        ) : null}
        {(results.data ?? []).map((item) => (
          <label key={item.id}>
            <input
              type={multiple ? 'checkbox' : 'radio'}
              name={name}
              checked={value.includes(item.id)}
              onChange={() => toggle(item)}
            />
            <span>{label(item)}</span>
          </label>
        ))}
      </fieldset>
    </div>
  );
}
