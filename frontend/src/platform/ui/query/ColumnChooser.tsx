import { useEffect, useRef, useState } from 'react';
import { useI18n } from '../../i18n/I18nProvider';
import { Button } from '../Button';

type Stored = { order: string[]; hidden: string[] };
export type ColumnInfo = { key: string; header: string };

const storageKey = (listKey: string) => `turaco.columns.${listKey}`;

/** Reads a saved layout; storage may be unavailable or hold stale data, so every step is defensive. */
export function readColumns(listKey: string): Stored {
  try {
    const parsed: unknown = JSON.parse(window.localStorage.getItem(storageKey(listKey)) ?? 'null');
    if (Array.isArray(parsed) && parsed.every((x) => typeof x === 'string'))
      return { order: parsed, hidden: [] };
    if (parsed && typeof parsed === 'object') {
      const { order, hidden } = parsed as Partial<Stored>;
      if (
        Array.isArray(order) &&
        Array.isArray(hidden) &&
        [...order, ...hidden].every((x) => typeof x === 'string')
      )
        return { order, hidden };
    }
  } catch {
    // Storage is optional.
  }
  return { order: [], hidden: [] };
}

/** Pure layout resolution: saved order first, new columns appended, unknown keys dropped. */
export function resolveColumns(
  all: readonly string[],
  saved: Stored,
): { order: string[]; hidden: string[] } {
  const order = [
    ...saved.order.filter((key) => all.includes(key)),
    ...all.filter((key) => !saved.order.includes(key)),
  ];
  const hidden = saved.hidden.filter((key) => all.includes(key));
  return { order, hidden: hidden.length >= order.length ? [] : hidden };
}

/** Show, hide and reorder columns. The layout is remembered per list in this browser only. */
export function ColumnChooser({
  listKey,
  columns,
  onChange,
  applied,
}: {
  listKey: string;
  columns: readonly ColumnInfo[];
  onChange: (visible: string[]) => void;
  /** Columns requested from outside (a Saved View); applied once per `id`, not stored locally. */
  applied?: { id: number; keys: readonly string[] } | undefined;
}) {
  const { t } = useI18n();
  const signature = columns.map((column) => column.key).join(',');
  const [layout, setLayout] = useState(() =>
    resolveColumns(signature.split(','), readColumns(listKey)),
  );
  const callback = useRef(onChange);
  callback.current = onChange;
  const keys = signature.split(',');
  const current = resolveColumns(keys, layout);
  const visible = current.order.filter((key) => !current.hidden.includes(key));
  const visibleSignature = visible.join(',');
  useEffect(() => {
    callback.current(visibleSignature ? visibleSignature.split(',') : []);
  }, [visibleSignature]);
  const appliedId = applied?.id;
  useEffect(() => {
    if (!applied || applied.id === 0) return;
    const wanted = applied.keys.filter((key) => keys.includes(key));
    if (wanted.length === 0) return;
    setLayout({
      order: [...wanted, ...keys.filter((key) => !wanted.includes(key))],
      hidden: keys.filter((key) => !wanted.includes(key)),
    });
    // Only a new request (id) applies; later layout edits by the user must stick.
  }, [appliedId]);
  const save = (next: { order: string[]; hidden: string[] }) => {
    setLayout(next);
    try {
      window.localStorage.setItem(storageKey(listKey), JSON.stringify(next));
    } catch {
      // Keep the layout in memory.
    }
  };
  const header = (key: string) => columns.find((column) => column.key === key)?.header ?? key;
  const move = (index: number, delta: number) => {
    const order = [...current.order];
    const [item] = order.splice(index, 1);
    if (item) order.splice(index + delta, 0, item);
    save({ order, hidden: current.hidden });
  };
  return (
    <details className="query-columns-box">
      <summary>{t('query.columns')}</summary>
      <ul className="query-columns">
        {current.order.map((key, index) => {
          const shown = !current.hidden.includes(key);
          return (
            <li key={key}>
              <label>
                <input
                  type="checkbox"
                  checked={shown}
                  disabled={shown && visible.length === 1}
                  onChange={() =>
                    save({
                      order: current.order,
                      hidden: shown
                        ? [...current.hidden, key]
                        : current.hidden.filter((item) => item !== key),
                    })
                  }
                />
                <span>{header(key)}</span>
              </label>
              <Button
                disabled={index === 0}
                aria-label={t('query.moveUp', { name: header(key) })}
                onClick={() => move(index, -1)}
              >
                {t('query.up')}
              </Button>
              <Button
                disabled={index === current.order.length - 1}
                aria-label={t('query.moveDown', { name: header(key) })}
                onClick={() => move(index, 1)}
              >
                {t('query.down')}
              </Button>
            </li>
          );
        })}
      </ul>
      <Button onClick={() => save({ order: keys, hidden: [] })}>{t('query.resetColumns')}</Button>
    </details>
  );
}
