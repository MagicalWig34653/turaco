import type { GroupRef } from './types';

/** Provider IDs are never a fallback for a name the API chose to hide. */
export function visibleGroupName(group: GroupRef | null): string | null {
  return group?.redacted ? null : (group?.name ?? null);
}

export function orderedCounts(keys: readonly string[], counts: Record<string, number>) {
  return keys.map((key) => ({ key, count: counts[key] ?? 0 }));
}
