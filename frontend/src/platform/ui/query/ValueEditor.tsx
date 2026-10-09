import { useState } from 'react';
import { useI18n } from '../../i18n/I18nProvider';
import {
  instantToLocal,
  isMultiValue,
  localToInstant,
  localZone,
  relativeOperators,
  unaryOperators,
  type Condition,
  type Field,
} from './filterModel';
import { ReferencePicker } from './ReferencePicker';
import { useQueryLabels } from './queryLabels';
import type { Catalog } from './filterModel';

type Props = {
  catalog: Catalog;
  field: Field;
  condition: Condition;
  onChange: (value: unknown) => void;
};

const toList = (value: unknown): unknown[] => (Array.isArray(value) ? value : []);

/** Comma separated values; keeps the raw text while typing so the caret never jumps. */
function ListInput({
  field,
  value,
  onChange,
}: {
  field: Field;
  value: unknown;
  onChange: (value: unknown) => void;
}) {
  const { t } = useI18n();
  const [text, setText] = useState(() => toList(value).join(', '));
  const numeric = field.type === 'number';
  return (
    <label>
      <span>{t('query.values')}</span>
      <input
        value={text}
        placeholder={t('query.valuesHint')}
        onChange={(event) => {
          setText(event.target.value);
          onChange(
            event.target.value
              .split(',')
              .map((item) => (numeric ? Number(item.trim()) : item.trim())),
          );
        }}
      />
    </label>
  );
}

function scalarInput(
  field: Field,
  op: string,
  current: unknown,
  label: string,
  update: (value: unknown) => void,
) {
  const relative = relativeOperators.has(op);
  const isDate = field.type === 'date' || field.type === 'datetime';
  const type =
    relative || field.type === 'number'
      ? 'number'
      : field.type === 'date'
        ? 'date'
        : field.type === 'datetime'
          ? 'datetime-local'
          : 'text';
  const shown =
    typeof current === 'number'
      ? current
      : typeof current === 'string'
        ? field.type === 'datetime' && !relative
          ? instantToLocal(current)
          : current
        : '';
  return (
    <label key={label}>
      <span>{label}</span>
      <input
        type={type}
        value={shown}
        maxLength={type === 'text' ? 200 : undefined}
        min={relative ? 1 : undefined}
        max={relative ? 3650 : undefined}
        step={relative ? 1 : undefined}
        onChange={(event) => {
          const raw = event.target.value;
          if (type === 'number') update(raw === '' ? '' : Number(raw));
          else if (isDate && field.type === 'datetime') update(localToInstant(raw));
          else update(raw);
        }}
      />
    </label>
  );
}

export function ValueEditor({ catalog, field, condition, onChange }: Props) {
  const { t } = useI18n();
  const labels = useQueryLabels(catalog);
  const { op, value } = condition;
  if (unaryOperators.has(op)) return null;
  const multi = isMultiValue(op);

  if (field.type === 'reference') {
    const ids = (multi ? toList(value) : [value]).filter(
      (id): id is string => typeof id === 'string' && id !== '',
    );
    return (
      <ReferencePicker
        resource={field.reference}
        value={ids}
        multiple={multi}
        onChange={(next) => onChange(multi ? next : (next[0] ?? ''))}
      />
    );
  }

  if (field.enumValues?.length) {
    if (!multi) {
      return (
        <label>
          <span>{t('query.value')}</span>
          <select value={String(value ?? '')} onChange={(event) => onChange(event.target.value)}>
            {field.enumValues.map((item) => (
              <option key={item} value={item}>
                {labels.valueLabel(item)}
              </option>
            ))}
          </select>
        </label>
      );
    }
    const selected = toList(value).map(String);
    return (
      <fieldset className="query-choices">
        <legend>{t('query.values')}</legend>
        {field.enumValues.map((item) => (
          <label key={item} className="query-choice">
            <input
              type="checkbox"
              checked={selected.includes(item)}
              onChange={() =>
                onChange(
                  selected.includes(item)
                    ? selected.filter((v) => v !== item)
                    : [...selected, item],
                )
              }
            />
            <span>{labels.valueLabel(item)}</span>
          </label>
        ))}
      </fieldset>
    );
  }

  const utc = field.type === 'datetime' && !relativeOperators.has(op);
  if (op === 'between') {
    const pair = toList(value);
    return (
      <>
        {scalarInput(field, op, pair[0], t('query.from'), (next) =>
          onChange([next, pair[1] ?? '']),
        )}
        {scalarInput(field, op, pair[1], t('query.to'), (next) => onChange([pair[0] ?? '', next]))}
        {utc ? (
          <small className="field-hint">{t('query.utcHint', { zone: localZone() })}</small>
        ) : null}
      </>
    );
  }
  if (multi) return <ListInput field={field} value={value} onChange={onChange} />;
  const label = relativeOperators.has(op)
    ? t('query.days')
    : utc
      ? t('query.utcValue')
      : t('query.value');
  return scalarInput(field, op, value, label, onChange);
}
