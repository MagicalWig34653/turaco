import type { CanFn } from '../../../platform/session/permissions';
import type { ViewDefinition } from '../../../platform/ui/views/api';
import {
  actionTarget,
  availableActions,
  isOverdue,
  reasonRequired,
  transitionAction,
} from '../actions';
import type { TaskAction, TaskPriority, TaskStatus } from '../types';
import type {
  Anchor,
  BoardCard,
  BoardColumn,
  BoardColumnDraft,
  BoardFilter,
  ColumnCards,
  TaskBoard,
} from './types';

/** Pure board logic: move planning, rank anchors, optimistic updates with rollback, lanes. */

export const MAX_COLUMNS = 8;
export const MIN_COLUMNS = 2;
export const MAX_TITLE = 40;
export const MAX_WIP = 999;

/** Cards per column as the board screen holds them, keyed by column id. */
export type CardsState = Record<string, ColumnCards>;

export const sortedColumns = (board: Pick<TaskBoard, 'columns'>): BoardColumn[] =>
  [...board.columns].sort((a, b) => a.position - b.position);

/** The column that lists unplaced cards of a status. */
export function firstColumnOfStatus(
  columns: readonly BoardColumn[],
  status: TaskStatus,
): BoardColumn | undefined {
  return [...columns].sort((a, b) => a.position - b.position).find((c) => c.mapsTo === status);
}

export type CardLocation = { columnId: string; index: number; card: BoardCard };

export function findCard(cards: CardsState, taskId: string): CardLocation | undefined {
  for (const [columnId, column] of Object.entries(cards)) {
    const index = column.items.findIndex((item) => item.id === taskId);
    const card = column.items[index];
    if (card) return { columnId, index, card };
  }
  return undefined;
}

/**
 * Rank anchor for a card dropped at `index` of a column's cards: below the card before the slot,
 * or first. The moving card itself never serves as an anchor.
 */
export function dropAnchor(
  items: readonly Pick<BoardCard, 'id'>[],
  index: number,
  movingId: string,
): Anchor {
  const others = items.filter((item) => item.id !== movingId);
  const before = others[Math.min(Math.max(index, 0), others.length) - 1];
  return before ? { afterTaskId: before.id } : { top: true };
}

/**
 * Slot (among the column's cards without the moving one) right below a card; used when a drop
 * lands in a lane or among filtered cards, where the visible neighbour is the anchor.
 */
export function slotAfter(
  items: readonly Pick<BoardCard, 'id'>[],
  movingId: string,
  precedingId: string | undefined,
): number {
  if (!precedingId) return 0;
  const others = items.filter((item) => item.id !== movingId);
  const at = others.findIndex((item) => item.id === precedingId);
  return at < 0 ? others.length : at + 1;
}

// ---- Move planning ---------------------------------------------------------------------------

export type MovePlan = {
  taskId: string;
  expectedVersion: number;
  fromColumnId: string;
  fromIndex: number;
  /** null: the card leaves the board (a quick action to a status without a column). */
  toColumnId: string | null;
  toStatus: TaskStatus;
  /** The lifecycle operation; null places the card inside its status (rank only). */
  action: TaskAction | null;
  needsReason: boolean;
  anchor: Anchor | undefined;
};

export type MoveRefusal =
  'noop' | 'invalid_transition' | 'no_permission' | 'needs_edit' | 'unknown';

export type MoveDecision = { ok: true; plan: MovePlan } | { ok: false; refusal: MoveRefusal };

/**
 * Plans a drop of a card on a column. A different status maps to the Task lifecycle operation of
 * the state machine (never a status write); the same status only changes the rank. `index` is the
 * slot in the target column's cards; without it the card goes to the top. Rank writes need Board
 * edit access; a plain move to the first column of a status needs none.
 */
export function planMove(input: {
  board: Pick<TaskBoard, 'columns' | 'canEdit'>;
  cards: CardsState;
  taskId: string;
  toColumnId: string;
  index?: number | undefined;
  can: CanFn;
}): MoveDecision {
  const { board, cards, taskId, toColumnId, can } = input;
  const from = findCard(cards, taskId);
  const target = board.columns.find((column) => column.id === toColumnId);
  if (!from || !target) return { ok: false, refusal: 'unknown' };
  const sameColumn = from.columnId === toColumnId;
  const action = transitionAction(from.card.status, target.mapsTo);
  const statusChange = from.card.status !== target.mapsTo;
  if (statusChange && !action) return { ok: false, refusal: 'invalid_transition' };
  if (action && !availableActions(from.card, can).includes(action))
    return { ok: false, refusal: 'no_permission' };

  const targetItems = cards[toColumnId]?.items ?? [];
  if (sameColumn && input.index !== undefined) {
    const slot = Math.min(Math.max(input.index, 0), targetItems.length - 1);
    if (slot === from.index) return { ok: false, refusal: 'noop' };
  }
  const firstOfStatus = firstColumnOfStatus(board.columns, target.mapsTo)?.id === toColumnId;
  const placed = !statusChange || !firstOfStatus || input.index !== undefined;
  if (placed && !board.canEdit) {
    // A read-only editor may still make the plain move to the status's first column.
    if (!statusChange || !firstOfStatus) return { ok: false, refusal: 'needs_edit' };
  }
  const wantsAnchor = board.canEdit && (placed || input.index === undefined);
  const anchor = wantsAnchor
    ? input.index === undefined
      ? ({ top: true } as const)
      : dropAnchor(targetItems, input.index, taskId)
    : undefined;
  return {
    ok: true,
    plan: {
      taskId,
      expectedVersion: from.card.version,
      fromColumnId: from.columnId,
      fromIndex: from.index,
      toColumnId,
      toStatus: target.mapsTo,
      action,
      needsReason: action !== null && reasonRequired.has(action),
      anchor,
    },
  };
}

/**
 * Plans a quick lifecycle action (complete, block, ...) as a move to the first column of its
 * status; when the board has no such column the card leaves the board.
 */
export function planAction(input: {
  board: Pick<TaskBoard, 'columns' | 'canEdit'>;
  cards: CardsState;
  taskId: string;
  action: TaskAction;
  can: CanFn;
}): MoveDecision {
  const { board, cards, taskId, action, can } = input;
  const from = findCard(cards, taskId);
  if (!from) return { ok: false, refusal: 'unknown' };
  if (!availableActions(from.card, can).includes(action))
    return { ok: false, refusal: 'no_permission' };
  const toStatus = actionTarget[action];
  const target = firstColumnOfStatus(board.columns, toStatus);
  if (target) return planMove({ board, cards, taskId, toColumnId: target.id, can });
  return {
    ok: true,
    plan: {
      taskId,
      expectedVersion: from.card.version,
      fromColumnId: from.columnId,
      fromIndex: from.index,
      toColumnId: null,
      toStatus,
      action,
      needsReason: reasonRequired.has(action),
      anchor: undefined,
    },
  };
}

export type MoveTarget = { column: BoardColumn; refusal: MoveRefusal | undefined };

/** Columns offered by the card menu "Move to" with the reason a column is unavailable. */
export function moveTargets(input: {
  board: Pick<TaskBoard, 'columns' | 'canEdit'>;
  cards: CardsState;
  taskId: string;
  can: CanFn;
}): MoveTarget[] {
  const from = findCard(input.cards, input.taskId);
  return sortedColumns(input.board)
    .filter((column) => column.id !== from?.columnId)
    .map((column) => {
      const decision = planMove({ ...input, toColumnId: column.id });
      return { column, refusal: decision.ok ? undefined : decision.refusal };
    });
}

export type KeyboardDirection = 'up' | 'down' | 'left' | 'right';

/** Slot a keyboard move (Alt+Arrow) aims at, or undefined at the edge of the board. */
export function keyboardTarget(
  board: Pick<TaskBoard, 'columns'>,
  cards: CardsState,
  taskId: string,
  direction: KeyboardDirection,
): { columnId: string; index: number } | undefined {
  const from = findCard(cards, taskId);
  if (!from) return undefined;
  if (direction === 'up' || direction === 'down') {
    const length = cards[from.columnId]?.items.length ?? 0;
    const index = from.index + (direction === 'up' ? -1 : 1);
    return index < 0 || index >= length ? undefined : { columnId: from.columnId, index };
  }
  const columns = sortedColumns(board);
  const at = columns.findIndex((column) => column.id === from.columnId);
  const next = columns[at + (direction === 'left' ? -1 : 1)];
  if (!next) return undefined;
  const length = cards[next.id]?.items.length ?? 0;
  return { columnId: next.id, index: Math.min(from.index, length) };
}

// ---- Optimistic update and rollback ----------------------------------------------------------

const withCount = (column: ColumnCards, delta: number): ColumnCards => {
  const count = Math.max(0, column.count + delta);
  return {
    ...column,
    count,
    overWipLimit: column.wipLimit !== null && count > column.wipLimit,
  };
};

/** Index at which an anchor puts a card in a column's cards (the card itself excluded). */
export function anchorIndex(items: readonly Pick<BoardCard, 'id'>[], anchor: Anchor | undefined) {
  if (!anchor || 'top' in anchor) return 0;
  const at = items.findIndex((item) => item.id === anchor.afterTaskId);
  return at < 0 ? items.length : at + 1;
}

/** Applies a planned move to the local cards at once; undo it with revertMove. */
export function applyMove(cards: CardsState, plan: MovePlan): CardsState {
  const from = findCard(cards, plan.taskId);
  const source = cards[plan.fromColumnId];
  if (!from || !source) return cards;
  const moved: BoardCard = { ...from.card, status: plan.toStatus };
  const remaining = source.items.filter((item) => item.id !== plan.taskId);
  if (plan.toColumnId === plan.fromColumnId) {
    const items = [...remaining];
    items.splice(anchorIndex(items, plan.anchor), 0, moved);
    return { ...cards, [plan.fromColumnId]: { ...source, items } };
  }
  const next: CardsState = {
    ...cards,
    [plan.fromColumnId]: withCount({ ...source, items: remaining }, -1),
  };
  if (plan.toColumnId === null) return next;
  const target = cards[plan.toColumnId];
  if (!target) return cards;
  const items = [...target.items];
  items.splice(anchorIndex(items, plan.anchor), 0, moved);
  next[plan.toColumnId] = withCount({ ...target, items }, 1);
  return next;
}

/** Undoes applyMove: the original card returns to its original slot; counts are restored. */
export function revertMove(cards: CardsState, plan: MovePlan, original: BoardCard): CardsState {
  const next: CardsState = { ...cards };
  const source = next[plan.fromColumnId];
  if (!source) return next;
  const sameColumn = plan.toColumnId === plan.fromColumnId;
  if (plan.toColumnId !== null && !sameColumn) {
    const target = next[plan.toColumnId];
    if (target) {
      next[plan.toColumnId] = withCount(
        { ...target, items: target.items.filter((item) => item.id !== plan.taskId) },
        -1,
      );
    }
  }
  const items = source.items.filter((item) => item.id !== plan.taskId);
  items.splice(Math.min(plan.fromIndex, items.length), 0, original);
  next[plan.fromColumnId] = sameColumn ? { ...source, items } : withCount({ ...source, items }, 1);
  return next;
}

/** Puts the server's answer (new version, status, rank) into the moved card. */
export function settleMove(
  cards: CardsState,
  taskId: string,
  task: Omit<BoardCard, 'rank'>,
  rank: string | null,
): CardsState {
  const found = findCard(cards, taskId);
  const column = found ? cards[found.columnId] : undefined;
  if (!found || !column) return cards;
  const items = [...column.items];
  items[found.index] = { ...task, rank };
  return { ...cards, [found.columnId]: { ...column, items } };
}

/** Stores a new rank on a card that was only placed. */
export function setRank(cards: CardsState, taskId: string, rank: string | null): CardsState {
  const found = findCard(cards, taskId);
  const column = found ? cards[found.columnId] : undefined;
  if (!found || !column) return cards;
  const items = [...column.items];
  items[found.index] = { ...found.card, rank };
  return { ...cards, [found.columnId]: { ...column, items } };
}

/** Replaces one card in place (assign to me, edit) without moving it. */
export function replaceCard(cards: CardsState, task: Omit<BoardCard, 'rank'>): CardsState {
  const found = findCard(cards, task.id);
  const column = found ? cards[found.columnId] : undefined;
  if (!found || !column) return cards;
  const items = [...column.items];
  items[found.index] = { ...task, rank: found.card.rank };
  return { ...cards, [found.columnId]: { ...column, items } };
}

/** Appends a further page of a column. */
export function appendPage(cards: CardsState, page: ColumnCards): CardsState {
  const column = cards[page.columnId];
  if (!column) return cards;
  const known = new Set(column.items.map((item) => item.id));
  const merged = {
    ...page,
    items: [...column.items, ...page.items.filter((item) => !known.has(item.id))],
  };
  return { ...cards, [page.columnId]: merged };
}

export function cardsFromResponse(columns: readonly ColumnCards[]): CardsState {
  return Object.fromEntries(columns.map((column) => [column.columnId, column]));
}

/**
 * Merges a background refresh: columns the viewer has paged into keep their cards (only counts
 * refresh), the others take the fresh first page.
 */
export function mergeRefresh(
  current: CardsState,
  fresh: CardsState,
  pagedColumns: ReadonlySet<string>,
): CardsState {
  const out: CardsState = {};
  for (const [id, column] of Object.entries(fresh)) {
    const old = current[id];
    out[id] =
      old && pagedColumns.has(id)
        ? {
            ...old,
            count: column.count,
            countCapped: column.countCapped,
            wipLimit: column.wipLimit,
            overWipLimit: column.overWipLimit,
          }
        : column;
  }
  return out;
}

// ---- WIP, counts, lanes, quick filter --------------------------------------------------------

export type WipState = 'none' | 'ok' | 'at' | 'over';

/** Soft WIP indicator: nothing is ever refused. */
export function wipState(
  column: Pick<ColumnCards, 'count' | 'wipLimit' | 'overWipLimit'>,
): WipState {
  if (column.wipLimit === null) return 'none';
  if (column.overWipLimit || column.count > column.wipLimit) return 'over';
  return column.count === column.wipLimit ? 'at' : 'ok';
}

export const countText = (column: Pick<ColumnCards, 'count' | 'countCapped'>): string =>
  `${column.count}${column.countCapped ? '+' : ''}`;

export const UNASSIGNED_LANE = '__unassigned__';

export const laneKeyOf = (card: Pick<BoardCard, 'assignedUserId' | 'assignedTeamId'>): string =>
  card.assignedUserId ?? (card.assignedTeamId ? `team:${card.assignedTeamId}` : UNASSIGNED_LANE);

export const laneLabelOf = (
  card: Pick<
    BoardCard,
    'assignedUserName' | 'assignedUserId' | 'assignedTeamName' | 'assignedTeamId'
  >,
): string =>
  card.assignedUserName ??
  card.assignedUserId ??
  card.assignedTeamName ??
  card.assignedTeamId ??
  '';

export type Lane = { key: string; label: string; cards: Record<string, BoardCard[]> };

/** Groups the loaded cards by assignee: named lanes alphabetically, the unassigned lane last. */
export function buildLanes(
  cards: Readonly<Record<string, readonly BoardCard[]>>,
  columnIds: readonly string[],
): Lane[] {
  const lanes = new Map<string, Lane>();
  for (const columnId of columnIds) {
    for (const card of cards[columnId] ?? []) {
      const key = laneKeyOf(card);
      let lane = lanes.get(key);
      if (!lane) {
        lane = {
          key,
          label: laneLabelOf(card),
          cards: Object.fromEntries(columnIds.map((id) => [id, []])),
        };
        lanes.set(key, lane);
      }
      lane.cards[columnId]?.push(card);
    }
  }
  return [...lanes.values()].sort((a, b) => {
    if (a.key === UNASSIGNED_LANE) return 1;
    if (b.key === UNASSIGNED_LANE) return -1;
    return a.label.localeCompare(b.label);
  });
}

export type QuickFilter = {
  text: string;
  mine: boolean;
  overdue: boolean;
  priority: TaskPriority | '';
};

export const emptyQuickFilter: QuickFilter = {
  text: '',
  mine: false,
  overdue: false,
  priority: '',
};

export const quickFilterActive = (filter: QuickFilter): boolean =>
  filter.text.trim() !== '' || filter.mine || filter.overdue || filter.priority !== '';

/** Client-side filter over the loaded cards; it never changes what the board stores. */
export function matchesQuickFilter(
  card: BoardCard,
  filter: QuickFilter,
  me: string | undefined,
  now: Date,
): boolean {
  const text = filter.text.trim().toLocaleLowerCase();
  if (text && !card.title.toLocaleLowerCase().includes(text)) return false;
  if (filter.mine && (me === undefined || card.assignedUserId !== me)) return false;
  if (filter.overdue && !isOverdue(card, now)) return false;
  if (filter.priority && card.priority !== filter.priority) return false;
  return true;
}

// ---- Board list, creation and column editor --------------------------------------------------

export type BoardSection = 'mine' | 'team' | 'shared';

export function boardSection(
  board: Pick<TaskBoard, 'ownerId' | 'ownerTeamId'>,
  me: string,
): BoardSection {
  if (board.ownerTeamId) return 'team';
  return board.ownerId === me ? 'mine' : 'shared';
}

export const presetStatuses: readonly TaskStatus[] = [
  'open',
  'in_progress',
  'blocked',
  'completed',
];

/** The columns of a new board; titles come from the caller's localized strings. */
export function presetColumns(
  titles: Record<'open' | 'in_progress' | 'blocked' | 'completed', string>,
): BoardColumnDraft[] {
  return presetStatuses.map((status) => ({
    title: titles[status as keyof typeof titles],
    mapsTo: status,
  }));
}

/** The board filter a Saved View's definition stands for (condition tree only). */
export function filterFromView(definition: ViewDefinition | undefined): BoardFilter | undefined {
  const root = definition?.filter?.root;
  return root ? { v: 1, root } : undefined;
}

export const canAddColumn = (board: Pick<TaskBoard, 'columns'>): boolean =>
  board.columns.length < MAX_COLUMNS;
export const canRemoveColumn = (board: Pick<TaskBoard, 'columns'>): boolean =>
  board.columns.length > MIN_COLUMNS;

export const validTitle = (title: string): boolean => {
  const trimmed = title.trim();
  return trimmed.length >= 1 && trimmed.length <= MAX_TITLE;
};

/** WIP input: empty clears the limit, 1 to 999 sets it; anything else is invalid. */
export function parseWip(text: string): number | null | 'invalid' {
  const trimmed = text.trim();
  if (trimmed === '') return null;
  if (!/^\d{1,3}$/.test(trimmed)) return 'invalid';
  const value = Number(trimmed);
  return value >= 1 && value <= MAX_WIP ? value : 'invalid';
}

/** A copy of a record without one key. */
export function omitKey<T>(record: Readonly<Record<string, T>>, key: string): Record<string, T> {
  return Object.fromEntries(Object.entries(record).filter(([name]) => name !== key));
}

/** True when a failed move should reload the board (stale versions, vanished card). */
export function shouldRefetchAfter(code: string): boolean {
  return (
    code === 'tasks.version_conflict' ||
    code === 'tasks.invalid_transition' ||
    code === 'tasks.not_found' ||
    code === 'tasks.board_conflict' ||
    code === 'tasks.board_anchor_invalid'
  );
}
