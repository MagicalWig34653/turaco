import type { Locale } from '../i18n/i18n';

/** Minutes to show for a Retry-After value; never less than one minute. */
export function retryAfterMinutes(seconds: number | undefined): number {
  if (seconds === undefined || !Number.isFinite(seconds) || seconds <= 0) return 1;
  return Math.max(1, Math.ceil(seconds / 60));
}

export function formatDateTime(locale: Locale, iso: string | null | undefined): string {
  if (!iso) return '–';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium' }).format(date);
}

/** Money from minor units (cents) with the currency; falls back to a plain number for unknown codes. */
export function formatMoney(locale: Locale, cents: number, currency: string): string {
  try {
    return new Intl.NumberFormat(locale, { style: 'currency', currency }).format(cents / 100);
  } catch {
    return `${(cents / 100).toFixed(2)} ${currency}`;
  }
}

/** Parses a user-entered decimal amount ("12,50" or "12.5") into minor units; undefined when invalid. */
export function parseMoneyToCents(input: string): number | undefined {
  const text = input.trim().replace(',', '.');
  if (!/^\d{1,9}(\.\d{1,2})?$/.test(text)) return undefined;
  const [whole, fraction = ''] = text.split('.');
  return Number(whole) * 100 + Number(fraction.padEnd(2, '0'));
}

/** Converts a <input type="datetime-local"> value (local time) to an RFC 3339 UTC string. */
export function localInputToIso(value: string): string | undefined {
  if (value.trim() === '') return undefined;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return undefined;
  return date.toISOString();
}

/** Converts an RFC 3339 string to a <input type="datetime-local"> value in local time; empty when invalid. */
export function isoToLocalInput(iso: string | null | undefined): string {
  if (!iso) return '';
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** Pretty JSON for display; undefined becomes "null". React escapes the result when rendered. */
export function formatJson(value: unknown): string {
  return JSON.stringify(value === undefined ? null : value, null, 2);
}

export type PermissionGroup<T> = { prefix: string; items: T[] };

/** Prefix of a permission name: everything before the last dot ("platform.roles.view" -> "platform.roles"). */
export function permissionPrefix(name: string): string {
  const index = name.lastIndexOf('.');
  return index > 0 ? name.slice(0, index) : name;
}

/** Groups items by permission prefix; groups and items are sorted by name. */
export function groupPermissions<T>(
  items: readonly T[],
  nameOf: (item: T) => string,
): PermissionGroup<T>[] {
  const groups = new Map<string, T[]>();
  for (const item of items) {
    const prefix = permissionPrefix(nameOf(item));
    const list = groups.get(prefix);
    if (list) list.push(item);
    else groups.set(prefix, [item]);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([prefix, list]) => ({
      prefix,
      items: [...list].sort((a, b) => nameOf(a).localeCompare(nameOf(b))),
    }));
}

const ROLE_KEY_PATTERN = /^[a-z0-9][a-z0-9-]{1,62}$/;
export function isValidRoleKey(key: string): boolean {
  return ROLE_KEY_PATTERN.test(key);
}

/** Headline counters of a directory sync run; empty when the run applied no snapshot. */
export function summarizeSyncCounts(counts: Record<string, number>): {
  usersObserved: number;
  usersCreated: number;
  usersUpdated: number;
  groupsObserved: number;
} | null {
  if (Object.keys(counts).length === 0) return null;
  return {
    usersObserved: counts.usersObserved ?? 0,
    usersCreated: counts.usersCreated ?? 0,
    usersUpdated: counts.usersUpdated ?? 0,
    groupsObserved: counts.groupsObserved ?? 0,
  };
}
