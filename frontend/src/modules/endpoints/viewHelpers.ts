import type { GroupRef } from './types';

/** Provider IDs are never a fallback for a name the API chose to hide. */
export function visibleGroupName(group: GroupRef | null): string | null {
  return group?.redacted ? null : (group?.name ?? null);
}

export function orderedCounts(keys: readonly string[], counts: Record<string, number>) {
  return keys.map((key) => ({ key, count: counts[key] ?? 0 }));
}

/** Unknown applicability must remain visible even when the server class is same. */
export function diffBadge(value: string, uncertain: boolean): string {
  return uncertain ? 'uncertain' : value;
}

export function historyChanges(changes: readonly string[]): string[] {
  return changes.filter((value) => ['target', 'mode', 'intent', 'filter'].includes(value));
}
