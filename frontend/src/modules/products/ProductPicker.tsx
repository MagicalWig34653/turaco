import { useId, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { productsApi } from './api';

export type ProductChoice = { id: string; name: string };

/** Search picker for active Products (needs products.view). */
export function ProductPicker({
  value,
  onChange,
}: {
  value: ProductChoice | null;
  onChange: (product: ProductChoice | null) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const name = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const allowed = can('products.view') || can('products.manage');
  const results = useAsync(
    async (signal) =>
      allowed
        ? (await productsApi.products({ q: debounced, active: true }, undefined, signal)).items
        : [],
    [debounced, allowed],
  );
  if (!allowed) return <p className="field-hint">{t('products.picker.noPermission')}</p>;
  return (
    <div className="picker">
      <TextField
        label={t('products.picker.search')}
        type="search"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        maxLength={100}
        autoComplete="off"
      />
      {value ? (
        <p className="picker-selected">{t('products.picker.selected', { name: value.name })}</p>
      ) : null}
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      <fieldset className="picker-results" aria-busy={results.loading}>
        <legend>{t('products.picker.results')}</legend>
        {results.loading ? <p role="status">{t('state.loading')}</p> : null}
        {!results.loading && results.data && results.data.length === 0 ? (
          <p className="empty">{t('products.picker.none')}</p>
        ) : null}
        {(results.data ?? []).map((product) => (
          <label key={product.id} className="picker-option">
            <input
              type="radio"
              name={name}
              checked={value?.id === product.id}
              onChange={() => onChange({ id: product.id, name: product.name })}
            />
            <span>{product.name}</span>
          </label>
        ))}
      </fieldset>
    </div>
  );
}
