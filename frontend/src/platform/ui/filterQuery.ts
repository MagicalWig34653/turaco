export type FilterQueryValues = Readonly<Record<string, string | boolean | null>>;

/** Own only the listed keys. Preserve unrelated route context and remove cleared filters. */
export function mergeFilterQuery(search: string, values: FilterQueryValues): string {
  const params = new URLSearchParams(search);
  for (const [key, value] of Object.entries(values)) {
    if (value === '' || value === null || value === false) params.delete(key);
    else params.set(key, String(value));
  }
  const result = params.toString();
  return result ? `?${result}` : '';
}
