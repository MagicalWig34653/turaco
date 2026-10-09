import { useI18n } from '../../i18n/I18nProvider';
import { Button } from '../Button';
import type { Catalog, Sort } from './filterModel';
import { useQueryLabels } from './queryLabels';

/** Ordered multi-column sort; the first entry is the primary key. */
export function SortEditor({
  catalog,
  sort,
  onChange,
}: {
  catalog: Catalog;
  sort: Sort[];
  onChange: (sort: Sort[]) => void;
}) {
  const { t } = useI18n();
  const labels = useQueryLabels(catalog);
  const max = catalog.limits.maxSortKeys ?? 3;
  const sortable = catalog.fields.filter((field) => field.sortable);
  const unused = sortable.find((field) => !sort.some((item) => item.field === field.key));
  const patch = (index: number, change: Partial<Sort>) =>
    onChange(sort.map((item, i) => (i === index ? { ...item, ...change } : item)));
  const move = (index: number, delta: number) => {
    const next = [...sort];
    const [item] = next.splice(index, 1);
    if (item) next.splice(index + delta, 0, item);
    onChange(next);
  };
  return (
    <fieldset>
      <legend>{t('query.sort')}</legend>
      {sort.length === 0 ? <p className="field-hint">{t('query.sortDefault')}</p> : null}
      {sort.map((item, index) => (
        <div className="query-sort" key={item.field}>
          <label>
            <span>{t('query.field')}</span>
            <select
              value={item.field}
              onChange={(event) => patch(index, { field: event.target.value })}
            >
              {sortable
                .filter(
                  (field) =>
                    field.key === item.field || !sort.some((other) => other.field === field.key),
                )
                .map((field) => (
                  <option key={field.key} value={field.key}>
                    {labels.fieldLabel(field.key)}
                  </option>
                ))}
            </select>
          </label>
          <label>
            <span>{t('query.direction')}</span>
            <select
              value={item.dir}
              onChange={(event) =>
                patch(index, { dir: event.target.value === 'desc' ? 'desc' : 'asc' })
              }
            >
              <option value="asc">{t('query.asc')}</option>
              <option value="desc">{t('query.desc')}</option>
            </select>
          </label>
          <label>
            <span>{t('query.nulls')}</span>
            <select
              value={item.nulls ?? 'last'}
              onChange={(event) =>
                patch(index, { nulls: event.target.value === 'first' ? 'first' : 'last' })
              }
            >
              <option value="first">{t('query.first')}</option>
              <option value="last">{t('query.last')}</option>
            </select>
          </label>
          <Button disabled={index === 0} onClick={() => move(index, -1)}>
            {t('query.up')}
          </Button>
          <Button disabled={index === sort.length - 1} onClick={() => move(index, 1)}>
            {t('query.down')}
          </Button>
          <Button onClick={() => onChange(sort.filter((_, i) => i !== index))}>
            {t('query.remove')}
          </Button>
        </div>
      ))}
      <Button
        disabled={sort.length >= max || !unused}
        onClick={() =>
          unused && onChange([...sort, { field: unused.key, dir: 'asc', nulls: 'last' }])
        }
      >
        {t('query.addSort')}
      </Button>
    </fieldset>
  );
}
