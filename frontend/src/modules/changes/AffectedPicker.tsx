import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { changesApi } from './api';
import { candidateKey, candidateLabel, maxAffected, toggleCandidate } from './helpers';
import type { AffectedCandidate } from './types';

const pickable = ['service', 'vm', 'asset'] as const;

/**
 * Multi-select picker for the resources a Change affects (GET /changes/affected-lookup).
 * Selections survive new searches and type switches. `linked` holds the `type:id` keys that are
 * already linked to the Change.
 */
export function AffectedPicker({
  selected,
  onChange,
  linked = [],
}: {
  selected: readonly AffectedCandidate[];
  onChange: (next: AffectedCandidate[]) => void;
  linked?: readonly string[];
}) {
  const { t } = useI18n();
  const [type, setType] = useState<AffectedCandidate['type']>('service');
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const results = useAsync(
    async (signal) =>
      debounced ? (await changesApi.affectedLookup(type, debounced, signal)).items : [],
    [type, debounced],
  );
  const full = selected.length + linked.length >= maxAffected;
  return (
    <div className="picker affected-picker">
      <div className="affected-picker-search">
        <Select
          label={t('changes.type')}
          value={type}
          onChange={(event) => setType(event.target.value as AffectedCandidate['type'])}
          options={pickable.map((value) => ({ value, label: t(`changes.type.${value}`) }))}
        />
        <TextField
          label={t('changes.pick.search')}
          hint={t(`changes.pick.hint.${type}` as MessageKey)}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
      </div>
      {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
      <fieldset className="picker-results" aria-busy={results.loading}>
        <legend className="visually-hidden">{t('changes.pick.results')}</legend>
        {!debounced ? <p className="field-hint">{t('changes.pick.idle')}</p> : null}
        {debounced && results.loading ? <p role="status">{t('state.loading')}</p> : null}
        {debounced && !results.loading && !results.error && results.data?.length === 0 ? (
          <p className="empty" role="status">
            {t('changes.pick.empty', { q: debounced })}
          </p>
        ) : null}
        {(debounced ? (results.data ?? []) : []).map((candidate) => {
          const key = candidateKey(candidate);
          const isLinked = linked.includes(key);
          const checked = selected.some((item) => candidateKey(item) === key);
          return (
            <label key={key} className="picker-option">
              <input
                type="checkbox"
                checked={checked || isLinked}
                disabled={isLinked || (!checked && full)}
                onChange={() => onChange(toggleCandidate(selected, candidate, linked.length))}
              />
              <span className="picker-text">
                <span>{candidateLabel(candidate)}</span>
                {isLinked ? (
                  <small className="picker-detail">{t('changes.pick.linked')}</small>
                ) : candidate.detail ? (
                  <small className="picker-detail">{candidate.detail}</small>
                ) : null}
              </span>
              <span className="picker-check" aria-hidden="true">
                ✓
              </span>
            </label>
          );
        })}
      </fieldset>
      {full ? (
        <p className="field-hint" role="status">
          {t('changes.pick.limit', { max: maxAffected })}
        </p>
      ) : null}
      <div className="affected-selected" aria-live="polite">
        <strong>{t('changes.pick.selected', { count: selected.length })}</strong>
        {selected.length === 0 ? (
          <p className="field-hint">{t('changes.pick.none')}</p>
        ) : (
          <ul className="affected-chips">
            {selected.map((item) => (
              <li key={candidateKey(item)} className="affected-chip">
                <small>{t(`changes.type.${item.type}`)}</small>
                <span>{candidateLabel(item)}</span>
                <Button
                  type="button"
                  aria-label={t('changes.pick.remove', { name: candidateLabel(item) })}
                  onClick={() =>
                    onChange(selected.filter((x) => candidateKey(x) !== candidateKey(item)))
                  }
                >
                  ×
                </Button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
