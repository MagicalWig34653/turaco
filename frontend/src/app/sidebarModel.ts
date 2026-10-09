import type { SidebarResponse, ViewCount, ViewPin } from '../platform/ui/views/api';
import { viewLink } from '../platform/ui/views/model';

/** Pure sidebar logic: persisted collapsed sections, pinned item order and route highlighting. */

export const PINNED_KEY = 'pinned';
/** Section of the built-in System Views (my open tickets, unassigned, one per Queue). */
export const QUEUES_KEY = 'queues';
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
  /** i18n key of a System View's name; `name` is empty then. */
  nameKey: string | undefined;
  /** Capped open-ticket count; undefined when it was not requested or could not be computed. */
  count: number | undefined;
  countCapped: boolean;
  /** `unavailable` is shown as a dash and is never read as zero. */
  countStatus: 'ok' | 'unavailable' | undefined;
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
        Number(b.source === 'system') - Number(a.source === 'system') ||
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
      nameKey: pin.nameKey,
      count:
        pin.countStatus === 'unavailable' || typeof pin.count !== 'number' ? undefined : pin.count,
      countCapped: pin.countCapped === true,
      countStatus: pin.countStatus,
    }));
}

/** The display cap of a sidebar badge; larger or capped counts read "99+". */
export const COUNT_DISPLAY_MAX = 99;

/**
 * Text for a count badge. `99+` when the server capped the count or it exceeds the badge width,
 * a dash when the count is unavailable (never 0), and undefined when no count was requested.
 */
export function countLabel(
  count: number | undefined,
  capped: boolean,
  status?: 'ok' | 'unavailable',
): string | undefined {
  if (status === 'unavailable') return '–';
  if (count === undefined) return undefined;
  return capped || count > COUNT_DISPLAY_MAX ? `${COUNT_DISPLAY_MAX}+` : String(count);
}

/** Ids of the entries whose count is refreshed through GET /views/counts (at most 30 per request). */
export function countIds(items: readonly Pick<PinnedItem, 'viewId' | 'countStatus' | 'count'>[]) {
  return items
    .filter((item) => item.count !== undefined || item.countStatus !== undefined)
    .map((item) => item.viewId)
    .slice(0, 30);
}

/** Merges fresh counts into a sidebar response; ids missing from the answer lose their count. */
export function applyCounts(
  sidebar: SidebarResponse,
  counts: readonly ViewCount[],
  requested: ReadonlySet<string>,
): SidebarResponse {
  const byId = new Map(counts.map((entry) => [entry.id, entry]));
  return {
    ...sidebar,
    groups: sidebar.groups.map((group) => ({
      ...group,
      items: group.items.map((pin) => {
        if (!requested.has(pin.viewId)) return pin;
        const rest: ViewPin = { ...pin };
        delete rest.count;
        delete rest.countCapped;
        delete rest.countStatus;
        const fresh = byId.get(pin.viewId);
        if (!fresh) return rest;
        if (fresh.status === 'unavailable' || typeof fresh.count !== 'number')
          return { ...rest, countStatus: 'unavailable' as const };
        return {
          ...rest,
          count: fresh.count,
          countCapped: fresh.capped === true,
          countStatus: 'ok' as const,
        };
      }),
    })),
  };
}

/** Pins a person can reorder or unpin; System Views are fixed. */
export const isEditablePin = (item: Pick<PinnedItem, 'source'>) => item.source !== 'system';

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
