import type { SidebarResponse, ViewPin } from '../platform/ui/views/api';
import { viewLink } from '../platform/ui/views/model';

/** Pure sidebar logic: persisted collapsed sections, pinned item order and route highlighting. */

export const PINNED_KEY = 'pinned';
const STORAGE_KEY = 'turaco.sidebar.collapsed';
const idPattern = /^[a-z][a-z0-9_]{0,39}$/;
const maxCollapsed = 20;

/** Only well-formed ids survive, deduplicated and bounded the way the server accepts them. */
export function sanitizeCollapsed(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return [
    ...new Set(
      value.filter((item): item is string => typeof item === 'string' && idPattern.test(item)),
    ),
  ].slice(0, maxCollapsed);
}

export function readCollapsedSections(): string[] {
  try {
    return sanitizeCollapsed(JSON.parse(window.localStorage.getItem(STORAGE_KEY) ?? '[]'));
  } catch {
    return [];
  }
}

export function writeCollapsedSections(keys: readonly string[]): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(sanitizeCollapsed(keys)));
  } catch {
    // The preference then lasts for this session only.
  }
}

export function toggleSection(collapsed: readonly string[], key: string): string[] {
  return collapsed.includes(key) ? collapsed.filter((item) => item !== key) : [...collapsed, key];
}

const groupOrder = ['work', 'tickets', 'tasks', 'endpoints'];

export type PinnedItem = {
  viewId: string;
  name: string;
  resource: string;
  groupKey: string;
  href: string;
  source: ViewPin['source'];
  /** Q-C slot: filled once GET /me/sidebar returns counts. */
  count: number | undefined;
  countCapped: boolean;
};

/** Visible pins of all groups in display order (group order, then position, then name). */
export function pinnedItems(sidebar: Pick<SidebarResponse, 'groups'> | undefined): PinnedItem[] {
  const rank = (key: string) => {
    const index = groupOrder.indexOf(key);
    return index === -1 ? groupOrder.length : index;
  };
  return (sidebar?.groups ?? [])
    .flatMap((group) => group.items.map((pin) => ({ ...pin, groupKey: pin.groupKey || group.key })))
    .filter((pin) => !pin.hidden)
    .sort(
      (a, b) =>
        rank(a.groupKey) - rank(b.groupKey) ||
        a.groupKey.localeCompare(b.groupKey) ||
        a.position - b.position ||
        a.name.localeCompare(b.name),
    )
    .map((pin) => ({
      viewId: pin.viewId,
      name: pin.name,
      resource: pin.resource,
      groupKey: pin.groupKey,
      href: viewLink(pin.resource, pin.viewId),
      source: pin.source,
      count: typeof pin.count === 'number' ? pin.count : undefined,
      countCapped: pin.countCapped === true,
    }));
}

/** Text for a count badge: capped counts read "99+"; undefined means no count is available. */
export function countLabel(count: number | undefined, capped: boolean): string | undefined {
  if (count === undefined) return undefined;
  return capped ? `${count}+` : String(count);
}

/** A pinned item is active on its list route while `?view=<id>` names it. */
export function isPinnedActive(
  item: Pick<PinnedItem, 'href' | 'viewId'>,
  pathname: string,
  search: string,
): boolean {
  const [path = ''] = item.href.split('?');
  return pathname === path && new URLSearchParams(search).get('view') === item.viewId;
}

/** Same list route as a pinned item whose View is open: the plain destination then stops claiming "current". */
export function pinnedActiveOn(
  items: readonly PinnedItem[],
  pathname: string,
  search: string,
): PinnedItem | undefined {
  return items.find((item) => isPinnedActive(item, pathname, search));
}
