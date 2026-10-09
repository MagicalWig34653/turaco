import {
  emptyState,
  listConditions,
  normalize,
  type Catalog,
  type Condition,
  type Node,
  type QueryState,
} from '../query/filterModel';
import type {
  PinPayload,
  SavedView,
  ShareLevel,
  ShareSubjectType,
  ViewDefinition,
  ViewPin,
  ViewResource,
  ViewShare,
  ViewWarning,
} from './api';

/** Pure view logic: definition <-> query state, dirty detection, abilities, shares and pins. */

const columnKey = /^[a-z][a-z0-9_]{0,39}$/;

export function stateToDefinition(state: QueryState, columns?: readonly string[]): ViewDefinition {
  const root = normalize(state.filter.root);
  const search = state.search.trim();
  const keys = (columns ?? []).filter((key) => columnKey.test(key)).slice(0, 40);
  return {
    filter: {
      v: 1,
      ...(root ? { root } : {}),
      ...(search ? { search } : {}),
      ...(state.sort.length ? { sort: state.sort } : {}),
    },
    ...(keys.length ? { columns: keys } : {}),
  };
}

export function definitionToState(definition: ViewDefinition | undefined): QueryState {
  const filter = definition?.filter;
  if (!filter) return emptyState();
  return {
    filter: filter.root ? { v: 1, root: filter.root } : { v: 1 },
    sort: filter.sort ?? [],
    search: filter.search ?? '',
  };
}

/** Canonical text of a query state: whitespace in search and empty groups never count as a change. */
function canonical(state: QueryState): string {
  const root = normalize(state.filter.root);
  return JSON.stringify([root ?? null, state.sort, state.search.trim()]);
}

export function sameQuery(a: QueryState, b: QueryState): boolean {
  return canonical(a) === canonical(b);
}

/**
 * True when the working filter, sort or columns differ from the saved View. Columns only count
 * when the View stores any, and only the ones this list still offers.
 */
export function isDirty(
  view: Pick<SavedView, 'definition'>,
  state: QueryState,
  visibleColumns: readonly string[] | undefined,
  offeredColumns: readonly string[],
): boolean {
  if (!sameQuery(definitionToState(view.definition), state)) return true;
  const saved = (view.definition.columns ?? []).filter((key) => offeredColumns.includes(key));
  if (saved.length === 0 || !visibleColumns) return false;
  return saved.join(',') !== visibleColumns.join(',');
}

export function isEmptyQuery(state: QueryState): boolean {
  return !normalize(state.filter.root) && state.sort.length === 0 && state.search.trim() === '';
}

/** Conditions the viewer's catalog cannot run: unknown or unfilterable field, operator or enum value. */
export function unavailableConditions(
  root: Node | undefined,
  catalog: Catalog | undefined,
): Array<{ node: Condition; path: number[] }> {
  if (!catalog) return [];
  return listConditions(root).filter(({ node }) => {
    const field = catalog.fields.find((candidate) => candidate.key === node.field);
    if (!field || !field.filterable || !field.operators.includes(node.op)) return true;
    if (!field.enumValues?.length || (field.type !== 'enum' && field.type !== 'tags')) return false;
    const values = Array.isArray(node.value) ? node.value : [node.value];
    return values.some((value) => typeof value === 'string' && !field.enumValues?.includes(value));
  });
}

/** Paths of server warnings ("root.children[1].children[0]") as condition paths. */
export function warningConditionPaths(warnings: readonly ViewWarning[]): number[][] {
  return warnings
    .filter(
      (warning) => warning.code === 'query.field_unavailable' && warning.path.startsWith('root'),
    )
    .map((warning) => [...warning.path.matchAll(/children\[(\d+)\]/g)].map((m) => Number(m[1])));
}

export function unavailableSortCount(warnings: readonly ViewWarning[]): number {
  return warnings.filter(
    (warning) => warning.code === 'query.field_unavailable' && warning.path.startsWith('sort'),
  ).length;
}

export type Abilities = {
  canRun: boolean;
  canEdit: boolean;
  canManage: boolean;
  canShare: boolean;
  canPin: boolean;
  canTakeOver: boolean;
  readOnly: boolean;
};
type Can = (permission: string) => boolean;

/** Presentation hints only; the backend re-checks every operation. */
export function abilities(view: Pick<SavedView, 'access'>, can: Can): Abilities {
  const owner = view.access === 'owner';
  const admin = view.access === 'admin';
  return {
    canRun: !admin,
    canEdit: owner || view.access === 'edit',
    canManage: owner || admin,
    canShare: (owner || admin) && (can('views.share') || can('views.publish')),
    canPin: !admin,
    canTakeOver: admin,
    readOnly: view.access === 'use',
  };
}

export type ViewSection = 'mine' | 'shared' | 'global';

/** Mine, shared with me, and global (everyone share, known to the owner/admin who sees the share list). */
export function sectionOf(view: SavedView): ViewSection {
  if (view.shares?.some((share) => share.subjectType === 'everyone')) return 'global';
  return view.access === 'owner' ? 'mine' : 'shared';
}

export function filterViews(views: readonly SavedView[], search: string): SavedView[] {
  const needle = search.trim().toLocaleLowerCase();
  if (!needle) return [...views];
  return views.filter((view) =>
    [view.name, view.description, view.ownerName ?? ''].some((text) =>
      text.toLocaleLowerCase().includes(needle),
    ),
  );
}

export function groupViews(views: readonly SavedView[]): Record<ViewSection, SavedView[]> {
  const out: Record<ViewSection, SavedView[]> = { mine: [], shared: [], global: [] };
  for (const view of views) out[sectionOf(view)].push(view);
  for (const list of Object.values(out)) list.sort((a, b) => a.name.localeCompare(b.name));
  return out;
}

export const sidebarGroupOf = (resource: ViewResource | string): string =>
  resource === 'devices' ? 'endpoints' : resource;

/** The list screen a View belongs to. */
export const viewPath = (resource: string): string =>
  ({ tickets: '/service-desk', devices: '/devices', tasks: '/tasks' })[resource] ?? '/';

export const viewLink = (resource: string, id: string): string =>
  `${viewPath(resource)}?view=${encodeURIComponent(id)}`;

// ---- Shares -------------------------------------------------------------------------------

export type ShareDraft = { subjectType: ShareSubjectType; subjectId?: string; level: ShareLevel };
export const maxShares = 50;

const shareId = (share: Pick<ShareDraft, 'subjectType' | 'subjectId'>) =>
  `${share.subjectType}:${share.subjectId ?? ''}`;

export function toDraft(shares: readonly ViewShare[] | undefined): ShareDraft[] {
  return (shares ?? []).map(({ subjectType, subjectId, level }) => ({
    subjectType,
    ...(subjectId ? { subjectId } : {}),
    level,
  }));
}

export function upsertShare(shares: readonly ShareDraft[], next: ShareDraft): ShareDraft[] {
  const level = next.subjectType === 'everyone' ? 'use' : next.level;
  const entry = { ...next, level };
  const found = shares.some((share) => shareId(share) === shareId(entry));
  return found
    ? shares.map((share) => (shareId(share) === shareId(entry) ? entry : share))
    : [...shares, entry];
}

export function removeShare(
  shares: readonly ShareDraft[],
  target: Pick<ShareDraft, 'subjectType' | 'subjectId'>,
): ShareDraft[] {
  return shares.filter((share) => shareId(share) !== shareId(target));
}

export function sharesChanged(a: readonly ShareDraft[], b: readonly ShareDraft[]): boolean {
  const text = (list: readonly ShareDraft[]) =>
    list
      .map((share) => `${shareId(share)}:${share.level}`)
      .sort()
      .join('|');
  return text(a) !== text(b);
}

/** Share rights the draft would need beyond removal: adding or raising needs views.share, everyone needs views.publish. */
export function shareProblems(
  saved: readonly ShareDraft[],
  draft: readonly ShareDraft[],
  can: Can,
): Array<'limit' | 'everyoneEdit' | 'needShare' | 'needPublish'> {
  const problems = new Set<'limit' | 'everyoneEdit' | 'needShare' | 'needPublish'>();
  if (draft.length > maxShares) problems.add('limit');
  for (const share of draft) {
    if (share.subjectType === 'everyone' && share.level === 'edit') problems.add('everyoneEdit');
    const before = saved.find((candidate) => shareId(candidate) === shareId(share));
    const added = !before;
    const raised = before?.level === 'use' && share.level === 'edit';
    if (!added && !raised) continue;
    if (share.subjectType === 'everyone') {
      if (!can('views.publish')) problems.add('needPublish');
    } else if (!can('views.share')) problems.add('needShare');
  }
  return [...problems];
}

export const hasEditShare = (shares: readonly ShareDraft[]) =>
  shares.some((share) => share.level === 'edit');

// ---- Pins ---------------------------------------------------------------------------------

/**
 * Body for PUT /me/pins from the merged pin state: user pins, plus rule pins the user hid or touched.
 * Positions are renumbered per group in list order.
 */
export function pinPayload(
  items: readonly ViewPin[],
  touched: ReadonlySet<string> = new Set(),
): PinPayload[] {
  const kept = items.filter(
    (pin) => pin.source === 'user' || pin.hidden || touched.has(pin.viewId),
  );
  const counters = new Map<string, number>();
  return kept.map((pin) => {
    const position = counters.get(pin.groupKey) ?? 0;
    counters.set(pin.groupKey, position + 1);
    return { viewId: pin.viewId, groupKey: pin.groupKey, position, hidden: pin.hidden };
  });
}

const byOrder = (a: ViewPin, b: ViewPin) =>
  a.groupKey.localeCompare(b.groupKey) || a.position - b.position || a.name.localeCompare(b.name);

export function addPin(
  items: readonly ViewPin[],
  view: Pick<SavedView, 'id' | 'name' | 'resource'>,
): ViewPin[] {
  const groupKey = sidebarGroupOf(view.resource);
  const existing = items.find((pin) => pin.viewId === view.id);
  if (existing)
    return items.map((pin) => (pin === existing ? { ...pin, source: 'user', hidden: false } : pin));
  const last = Math.max(
    -1,
    ...items.filter((pin) => pin.groupKey === groupKey).map((pin) => pin.position),
  );
  return [
    ...items,
    {
      viewId: view.id,
      name: view.name,
      resource: view.resource,
      groupKey,
      position: last + 1,
      hidden: false,
      source: 'user',
    },
  ];
}

/** A user pin disappears; a rule pin can only be hidden for the caller. */
export function removePin(items: readonly ViewPin[], viewId: string): ViewPin[] {
  return items.flatMap((pin): ViewPin[] => {
    if (pin.viewId !== viewId) return [pin];
    return pin.source === 'rule' ? [{ ...pin, hidden: true }] : [];
  });
}

/** Moves a visible pin one place within its group; null when it is already at that end. */
export function movePin(
  items: readonly ViewPin[],
  viewId: string,
  delta: -1 | 1,
): { items: ViewPin[]; touched: Set<string> } | null {
  const target = items.find((pin) => pin.viewId === viewId);
  if (!target) return null;
  const group = items
    .filter((pin) => pin.groupKey === target.groupKey && !pin.hidden)
    .sort(byOrder);
  const index = group.findIndex((pin) => pin.viewId === viewId);
  const neighbour = group[index + delta];
  if (index < 0 || !neighbour) return null;
  const reordered = [...group];
  reordered[index] = neighbour;
  reordered[index + delta] = target;
  const positions = new Map(reordered.map((pin, i) => [pin.viewId, i]));
  return {
    items: items
      .map((pin) =>
        positions.has(pin.viewId) ? { ...pin, position: positions.get(pin.viewId) ?? 0 } : pin,
      )
      .sort(byOrder),
    touched: new Set(group.map((pin) => pin.viewId)),
  };
}
