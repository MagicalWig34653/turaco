import { useI18n } from '../../i18n/I18nProvider';
import type { MessageKey } from '../../i18n/i18n';
import type { Catalog, Condition, Field } from './filterModel';
import { isMultiValue, relativeOperators, unaryOperators } from './filterModel';
import type { ReferenceKind } from './referenceNames';
import { referenceKind } from './referenceNames';

/** Localized names for catalog fields, operators and enum values; unknown ids fall back to themselves. */
export function useQueryLabels(catalog: Catalog | undefined) {
  const { t } = useI18n();
  const known = (key: string, fallback: string) => {
    const text = t(key as MessageKey);
    return text === key ? fallback : text;
  };
  const resource = catalog?.resource ?? '';
  const fieldLabel = (key: string) => known(`query.field.${resource}.${key}`, key);
  const opLabel = (op: string) => known(`query.op.${op}`, op);
  const valueLabel = (value: string) => known(`query.enum.${value}`, value);
  return {
    fieldLabel,
    opLabel,
    valueLabel,
    /** Human-readable chip text for one condition; references resolve through `resolve`. */
    describe(
      condition: Condition,
      field: Field | undefined,
      resolve: (kind: ReferenceKind, id: string) => string | undefined,
    ): string {
      const head = `${fieldLabel(condition.field)} ${opLabel(condition.op)}`;
      if (relativeOperators.has(condition.op)) {
        const count = typeof condition.value === 'number' ? condition.value : '…';
        return `${fieldLabel(condition.field)} ${t(`query.opChip.${condition.op}` as MessageKey, { count })}`;
      }
      if (unaryOperators.has(condition.op)) return head;
      const values = Array.isArray(condition.value) ? condition.value : [condition.value];
      const kind = referenceKind(field?.reference);
      const shown = values.map((value) => {
        if (value === undefined || value === null || value === '') return '…';
        if (kind) return resolve(kind, String(value)) ?? t('query.referenceUnknown');
        if (field?.enumValues) return valueLabel(String(value));
        if (field?.type === 'boolean') return String(value);
        return String(value);
      });
      if (condition.op === 'between') return `${head} ${shown.join(' – ')}`;
      return `${head} ${isMultiValue(condition.op) ? shown.join(', ') : shown[0]}`;
    },
  };
}
