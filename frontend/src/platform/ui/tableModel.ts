export type SortValue = string | number | null | undefined;

/** Keep missing values last in either direction; never mutate the loaded page. */
export function sortRows<T>(
  rows: readonly T[],
  value: (row: T) => SortValue,
  direction: 'asc' | 'desc',
  locale: string,
): T[] {
  const collator = new Intl.Collator(locale, { numeric: true, sensitivity: 'base' });
  return [...rows].sort((a, b) => {
    const left = value(a),
      right = value(b);
    if (left == null) return right == null ? 0 : 1;
    if (right == null) return -1;
    const result =
      typeof left === 'number' && typeof right === 'number'
        ? left - right
        : collator.compare(String(left), String(right));
    return direction === 'asc' ? result : -result;
  });
}

export function relativeDate(iso: string, locale: string, now: number): string {
  const timestamp = Date.parse(iso);
  if (!Number.isFinite(timestamp)) return iso;
  const seconds = (timestamp - now) / 1000;
  const unit =
    Math.abs(seconds) < 60
      ? 'second'
      : Math.abs(seconds) < 3600
        ? 'minute'
        : Math.abs(seconds) < 86400
          ? 'hour'
          : 'day';
  const divisor = { second: 1, minute: 60, hour: 3600, day: 86400 }[unit];
  return new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(
    Math.round(seconds / divisor),
    unit,
  );
}
