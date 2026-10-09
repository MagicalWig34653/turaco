import { describe, expect, it } from 'vitest';
import { createCan } from '../../../platform/session/permissions';
import { transitionAction } from '../actions';
import type { TaskStatus } from '../types';
import {
  anchorIndex,
  appendPage,
  applyMove,
  boardSection,
  buildLanes,
  canAddColumn,
  canRemoveColumn,
  countText,
  dropAnchor,
  emptyQuickFilter,
  filterFromView,
  findCard,
  firstColumnOfStatus,
  keyboardTarget,
  matchesQuickFilter,
  mergeRefresh,
  moveTargets,
  parseWip,
  planAction,
  planMove,
  presetColumns,
  replaceCard,
  slotAfter,
  revertMove,
  settleMove,
  shouldRefetchAfter,
  UNASSIGNED_LANE,
  validTitle,
  wipState,
  type CardsState,
} from './model';
import type { BoardCard, BoardColumn, ColumnCards, TaskBoard } from './types';

const manager = createCan({ permissions: ['tasks.manage', 'tasks.work', 'tasks.view'] });
const worker = createCan({ permissions: ['tasks.work', 'tasks.view'] });

const column = (
  id: string,
  position: number,
  mapsTo: TaskStatus,
  wipLimit: number | null = null,
): BoardColumn => ({
  id,
  position,
  title: id,
  mapsTo,
  collapsed: false,
  wipLimit,
});

const card = (id: string, status: TaskStatus, extra: Partial<BoardCard> = {}): BoardCard => ({
  id,
  title: `Task ${id}`,
  description: null,
  status,
  statusReason: null,
  priority: 'normal',
  assignedUserId: null,
  assignedUserName: null,
  assignedTeamId: null,
  assignedTeamName: null,
  contextType: null,
  contextId: null,
  dueAt: null,
  completedAt: null,
  createdByUserId: null,
  completedByUserId: null,
  version: 3,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  rank: null,
  ...extra,
});

const bucket = (
  columnId: string,
  items: BoardCard[],
  extra: Partial<ColumnCards> = {},
): ColumnCards => ({
  columnId,
  items,
  count: items.length,
  countCapped: false,
  wipLimit: null,
  overWipLimit: false,
  ...extra,
});

const columns = [
  column('todo', 0, 'open'),
  column('doing', 1, 'in_progress'),
  column('stuck', 2, 'blocked'),
  column('done', 3, 'completed'),
  column('later', 4, 'open'),
];

const board = (canEdit = true): Pick<TaskBoard, 'columns' | 'canEdit'> => ({ columns, canEdit });

const state = (): CardsState => ({
  todo: bucket('todo', [card('a', 'open'), card('b', 'open'), card('c', 'open')]),
  doing: bucket('doing', [card('d', 'in_progress')]),
  stuck: bucket('stuck', [card('e', 'blocked')]),
  done: bucket('done', [card('f', 'completed')]),
  later: bucket('later', []),
});

describe('transitionAction', () => {
  it('maps a status pair to the lifecycle operation', () => {
    expect(transitionAction('open', 'in_progress')).toBe('start');
    expect(transitionAction('blocked', 'in_progress')).toBe('start');
    expect(transitionAction('in_progress', 'completed')).toBe('complete');
    expect(transitionAction('open', 'blocked')).toBe('block');
    expect(transitionAction('blocked', 'open')).toBe('unblock');
    expect(transitionAction('completed', 'open')).toBe('reopen');
    expect(transitionAction('cancelled', 'open')).toBe('reopen');
    expect(transitionAction('blocked', 'cancelled')).toBe('cancel');
  });
  it('has no operation for forbidden pairs or an unchanged status', () => {
    expect(transitionAction('completed', 'in_progress')).toBeNull();
    expect(transitionAction('in_progress', 'open')).toBeNull();
    expect(transitionAction('blocked', 'completed')).toBeNull();
    expect(transitionAction('open', 'open')).toBeNull();
  });
});

describe('dropAnchor', () => {
  const items = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
  it('anchors below the card before the slot', () => {
    expect(dropAnchor(items, 0, 'x')).toEqual({ top: true });
    expect(dropAnchor(items, 1, 'x')).toEqual({ afterTaskId: 'a' });
    expect(dropAnchor(items, 3, 'x')).toEqual({ afterTaskId: 'c' });
    expect(dropAnchor(items, 99, 'x')).toEqual({ afterTaskId: 'c' });
  });
  it('never anchors on the moving card', () => {
    expect(dropAnchor(items, 2, 'b')).toEqual({ afterTaskId: 'c' });
    expect(dropAnchor(items, 1, 'b')).toEqual({ afterTaskId: 'a' });
    expect(dropAnchor(items, 1, 'a')).toEqual({ afterTaskId: 'b' });
    expect(dropAnchor(items, 0, 'a')).toEqual({ top: true });
  });
  it('resolves an anchor back to its slot', () => {
    expect(anchorIndex(items, { top: true })).toBe(0);
    expect(anchorIndex(items, { afterTaskId: 'b' })).toBe(2);
    expect(anchorIndex(items, { afterTaskId: 'gone' })).toBe(3);
    expect(anchorIndex(items, undefined)).toBe(0);
  });
});

describe('slotAfter', () => {
  const items = [{ id: 'a' }, { id: 'b' }, { id: 'c' }];
  it('finds the slot below a visible neighbour among all cards', () => {
    expect(slotAfter(items, 'x', undefined)).toBe(0);
    expect(slotAfter(items, 'x', 'b')).toBe(2);
    expect(slotAfter(items, 'a', 'c')).toBe(2);
    expect(slotAfter(items, 'x', 'gone')).toBe(3);
  });
});

describe('planMove', () => {
  const plan = (toColumnId: string, taskId: string, index?: number, can = manager, edit = true) =>
    planMove({ board: board(edit), cards: state(), taskId, toColumnId, index, can });

  it('plans start for open to in progress with the drop anchor', () => {
    const decision = plan('doing', 'a', 1);
    expect(decision).toMatchObject({
      ok: true,
      plan: {
        action: 'start',
        needsReason: false,
        toColumnId: 'doing',
        fromColumnId: 'todo',
        expectedVersion: 3,
        anchor: { afterTaskId: 'd' },
      },
    });
  });

  it('requires a reason for block, cancel and reopen', () => {
    expect(plan('stuck', 'a')).toMatchObject({
      ok: true,
      plan: { action: 'block', needsReason: true },
    });
    expect(plan('todo', 'f')).toMatchObject({
      ok: true,
      plan: { action: 'reopen', needsReason: true },
    });
    expect(plan('doing', 'e')).toMatchObject({
      ok: true,
      plan: { action: 'start', needsReason: false },
    });
  });

  it('refuses transitions the state machine forbids before dropping', () => {
    expect(plan('doing', 'f')).toEqual({ ok: false, refusal: 'invalid_transition' });
    expect(plan('done', 'e')).toEqual({ ok: false, refusal: 'invalid_transition' });
    expect(plan('todo', 'd')).toEqual({ ok: false, refusal: 'invalid_transition' });
  });

  it('refuses what the task permissions do not allow', () => {
    expect(plan('todo', 'f', undefined, worker)).toEqual({ ok: false, refusal: 'no_permission' });
    expect(plan('doing', 'a', undefined, worker)).toMatchObject({ ok: true });
  });

  it('only ranks inside a column and treats the same slot as no change', () => {
    expect(plan('todo', 'a', 0)).toEqual({ ok: false, refusal: 'noop' });
    expect(plan('todo', 'a', 1)).toMatchObject({
      ok: true,
      plan: { action: null, anchor: { afterTaskId: 'b' } },
    });
    expect(plan('todo', 'c', 0)).toMatchObject({
      ok: true,
      plan: { action: null, anchor: { top: true } },
    });
  });

  it('treats a second column of the same status as a placement', () => {
    expect(plan('later', 'a', 0)).toMatchObject({
      ok: true,
      plan: { action: null, toColumnId: 'later' },
    });
    expect(firstColumnOfStatus(columns, 'open')?.id).toBe('todo');
  });

  it('needs board edit access for ranks but not for a plain move to the first column', () => {
    expect(plan('todo', 'a', 1, manager, false)).toEqual({ ok: false, refusal: 'needs_edit' });
    expect(plan('later', 'a', 0, manager, false)).toEqual({ ok: false, refusal: 'needs_edit' });
    const plain = plan('doing', 'a', 1, manager, false);
    expect(plain).toMatchObject({ ok: true, plan: { action: 'start' } });
    expect(plain.ok && plain.plan.anchor).toBeUndefined();
  });

  it('places a menu move on top for editors', () => {
    expect(plan('doing', 'a')).toMatchObject({ ok: true, plan: { anchor: { top: true } } });
  });

  it('is unknown for a missing card or column', () => {
    expect(plan('nowhere', 'a')).toEqual({ ok: false, refusal: 'unknown' });
    expect(plan('doing', 'zzz')).toEqual({ ok: false, refusal: 'unknown' });
  });
});

describe('planAction', () => {
  it('moves to the first column of the action status', () => {
    const decision = planAction({
      board: board(),
      cards: state(),
      taskId: 'a',
      action: 'complete',
      can: manager,
    });
    expect(decision).toMatchObject({ ok: true, plan: { toColumnId: 'done', action: 'complete' } });
  });
  it('leaves the board when no column stands for the status', () => {
    const decision = planAction({
      board: board(),
      cards: state(),
      taskId: 'a',
      action: 'cancel',
      can: manager,
    });
    expect(decision).toMatchObject({
      ok: true,
      plan: { toColumnId: null, action: 'cancel', needsReason: true },
    });
  });
  it('refuses an action the viewer may not run', () => {
    expect(
      planAction({ board: board(), cards: state(), taskId: 'a', action: 'cancel', can: worker }),
    ).toEqual({
      ok: false,
      refusal: 'no_permission',
    });
  });
});

describe('moveTargets', () => {
  it('lists every other column with the reason it is unavailable', () => {
    const targets = moveTargets({ board: board(), cards: state(), taskId: 'f', can: manager });
    expect(targets.map((target) => target.column.id)).toEqual(['todo', 'doing', 'stuck', 'later']);
    expect(targets.find((target) => target.column.id === 'todo')?.refusal).toBeUndefined();
    expect(targets.find((target) => target.column.id === 'doing')?.refusal).toBe(
      'invalid_transition',
    );
  });
});

describe('keyboardTarget', () => {
  it('moves within a column and across columns', () => {
    const cards = state();
    expect(keyboardTarget(board(), cards, 'b', 'up')).toEqual({ columnId: 'todo', index: 0 });
    expect(keyboardTarget(board(), cards, 'b', 'down')).toEqual({ columnId: 'todo', index: 2 });
    expect(keyboardTarget(board(), cards, 'a', 'up')).toBeUndefined();
    expect(keyboardTarget(board(), cards, 'c', 'down')).toBeUndefined();
    expect(keyboardTarget(board(), cards, 'c', 'right')).toEqual({ columnId: 'doing', index: 1 });
    expect(keyboardTarget(board(), cards, 'a', 'left')).toBeUndefined();
  });
});

describe('optimistic move and rollback', () => {
  const run = (toColumnId: string, taskId: string, index?: number) => {
    const cards = state();
    const decision = planMove({ board: board(), cards, taskId, toColumnId, index, can: manager });
    if (!decision.ok) throw new Error(decision.refusal);
    return { cards, plan: decision.plan };
  };

  it('moves the card, updates its status and the counts', () => {
    const { cards, plan } = run('doing', 'a', 0);
    const next = applyMove(cards, plan);
    expect(next.todo?.items.map((item) => item.id)).toEqual(['b', 'c']);
    expect(next.doing?.items.map((item) => item.id)).toEqual(['a', 'd']);
    expect(findCard(next, 'a')?.card.status).toBe('in_progress');
    expect([next.todo?.count, next.doing?.count]).toEqual([2, 2]);
    expect(cards.todo?.items).toHaveLength(3);
  });

  it('rolls a cross-column move back exactly', () => {
    const { cards, plan } = run('doing', 'b', 1);
    const original = findCard(cards, 'b')?.card as BoardCard;
    expect(revertMove(applyMove(cards, plan), plan, original)).toEqual(cards);
  });

  it('rolls a reorder back exactly and keeps the count', () => {
    const { cards, plan } = run('todo', 'a', 2);
    const next = applyMove(cards, plan);
    expect(next.todo?.items.map((item) => item.id)).toEqual(['b', 'c', 'a']);
    expect(next.todo?.count).toBe(3);
    expect(revertMove(next, plan, findCard(cards, 'a')?.card as BoardCard)).toEqual(cards);
  });

  it('rolls back a move that leaves the board', () => {
    const cards = state();
    const decision = planAction({
      board: board(),
      cards,
      taskId: 'c',
      action: 'cancel',
      can: manager,
    });
    if (!decision.ok) throw new Error('plan');
    const next = applyMove(cards, decision.plan);
    expect(findCard(next, 'c')).toBeUndefined();
    expect(next.todo?.count).toBe(2);
    expect(revertMove(next, decision.plan, findCard(cards, 'c')?.card as BoardCard)).toEqual(cards);
  });

  it('keeps unrelated optimistic moves when one fails', () => {
    const first = run('doing', 'a', 0);
    const afterFirst = applyMove(first.cards, first.plan);
    const second = planMove({
      board: board(),
      cards: afterFirst,
      taskId: 'e',
      toColumnId: 'doing',
      index: 0,
      can: manager,
    });
    if (!second.ok) throw new Error('plan');
    const afterSecond = applyMove(afterFirst, second.plan);
    const undone = revertMove(
      afterSecond,
      first.plan,
      findCard(first.cards, 'a')?.card as BoardCard,
    );
    expect(undone.doing?.items.map((item) => item.id)).toEqual(['e', 'd']);
    expect(undone.todo?.items.map((item) => item.id)).toEqual(['a', 'b', 'c']);
  });

  it('flags a column over its soft WIP limit', () => {
    const cards = state();
    cards.doing = bucket('doing', [card('d', 'in_progress')], { wipLimit: 1 });
    const decision = planMove({
      board: board(),
      cards,
      taskId: 'a',
      toColumnId: 'doing',
      index: 0,
      can: manager,
    });
    if (!decision.ok) throw new Error('plan');
    const next = applyMove(cards, decision.plan);
    expect(next.doing?.overWipLimit).toBe(true);
    expect(wipState(next.doing as ColumnCards)).toBe('over');
  });

  it('settles with the server version and rank', () => {
    const { cards, plan } = run('doing', 'a', 0);
    const moved = applyMove(cards, plan);
    const settled = settleMove(moved, 'a', card('a', 'in_progress', { version: 4 }), 'V');
    expect(findCard(settled, 'a')?.card).toMatchObject({
      version: 4,
      rank: 'V',
      status: 'in_progress',
    });
  });
});

describe('cards state helpers', () => {
  it('replaces a card in place and appends pages without duplicates', () => {
    const cards = state();
    const changed = card('b', 'open', { title: 'Renamed', version: 9 });
    expect(findCard(replaceCard(cards, changed), 'b')?.card).toMatchObject({
      title: 'Renamed',
      version: 9,
    });
    const paged = appendPage(cards, bucket('todo', [card('c', 'open'), card('z', 'open')]));
    expect(paged.todo?.items.map((item) => item.id)).toEqual(['a', 'b', 'c', 'z']);
  });

  it('refreshes only counts of columns the viewer paged into', () => {
    const current = state();
    const fresh = { ...state(), todo: bucket('todo', [card('new', 'open')], { count: 40 }) };
    const merged = mergeRefresh(current, fresh, new Set(['todo']));
    expect(merged.todo?.items).toHaveLength(3);
    expect(merged.todo?.count).toBe(40);
    expect(mergeRefresh(current, fresh, new Set()).todo?.items.map((item) => item.id)).toEqual([
      'new',
    ]);
  });
});

describe('wip and counts', () => {
  it('reports none, ok, at and over', () => {
    expect(wipState({ count: 9, wipLimit: null, overWipLimit: false })).toBe('none');
    expect(wipState({ count: 1, wipLimit: 3, overWipLimit: false })).toBe('ok');
    expect(wipState({ count: 3, wipLimit: 3, overWipLimit: false })).toBe('at');
    expect(wipState({ count: 4, wipLimit: 3, overWipLimit: false })).toBe('over');
    expect(wipState({ count: 2, wipLimit: 3, overWipLimit: true })).toBe('over');
  });
  it('marks capped counts', () => {
    expect(countText({ count: 1000, countCapped: true })).toBe('1000+');
    expect(countText({ count: 7, countCapped: false })).toBe('7');
  });
});

describe('swimlanes', () => {
  it('groups loaded cards by assignee with the unassigned lane last', () => {
    const cards = {
      todo: [
        card('1', 'open', { assignedUserId: 'u2', assignedUserName: 'Zoe' }),
        card('2', 'open'),
        card('3', 'open', { assignedUserId: 'u1', assignedUserName: 'Ada' }),
      ],
      doing: [card('4', 'in_progress', { assignedUserId: 'u1', assignedUserName: 'Ada' })],
    };
    const lanes = buildLanes(cards, ['todo', 'doing']);
    expect(lanes.map((lane) => lane.label)).toEqual(['Ada', 'Zoe', '']);
    expect(lanes[2]?.key).toBe(UNASSIGNED_LANE);
    expect(lanes[0]?.cards.todo?.map((item) => item.id)).toEqual(['3']);
    expect(lanes[0]?.cards.doing?.map((item) => item.id)).toEqual(['4']);
    expect(lanes[1]?.cards.doing).toEqual([]);
  });
});

describe('quick filter', () => {
  const now = new Date('2026-10-09T12:00:00Z');
  it('combines text, mine, overdue and priority', () => {
    const late = card('x', 'open', {
      title: 'Patch the VPN',
      dueAt: '2026-10-01T00:00:00Z',
      assignedUserId: 'me',
      priority: 'high',
    });
    expect(matchesQuickFilter(late, emptyQuickFilter, 'me', now)).toBe(true);
    expect(matchesQuickFilter(late, { ...emptyQuickFilter, text: 'vpn' }, 'me', now)).toBe(true);
    expect(matchesQuickFilter(late, { ...emptyQuickFilter, text: 'printer' }, 'me', now)).toBe(
      false,
    );
    expect(matchesQuickFilter(late, { ...emptyQuickFilter, mine: true }, 'other', now)).toBe(false);
    expect(matchesQuickFilter(late, { ...emptyQuickFilter, overdue: true }, 'me', now)).toBe(true);
    expect(matchesQuickFilter(late, { ...emptyQuickFilter, priority: 'low' }, 'me', now)).toBe(
      false,
    );
  });
});

describe('board list and editor helpers', () => {
  it('sorts boards into mine, team and shared', () => {
    expect(boardSection({ ownerId: 'me', ownerTeamId: null }, 'me')).toBe('mine');
    expect(boardSection({ ownerId: 'me', ownerTeamId: 't' }, 'me')).toBe('team');
    expect(boardSection({ ownerId: 'x', ownerTeamId: null }, 'me')).toBe('shared');
  });
  it('builds the preset columns and the filter of a view', () => {
    const preset = presetColumns({
      open: 'To do',
      in_progress: 'In progress',
      blocked: 'Blocked',
      completed: 'Done',
    });
    expect(preset.map((entry) => entry.mapsTo)).toEqual([
      'open',
      'in_progress',
      'blocked',
      'completed',
    ]);
    expect(preset[0]?.title).toBe('To do');
    expect(filterFromView({ filter: { v: 1 } })).toBeUndefined();
    const root = { type: 'condition' as const, field: 'priority', op: 'eq', value: 'high' };
    expect(filterFromView({ filter: { v: 1, root, search: 'x' } })).toEqual({ v: 1, root });
  });
  it('enforces 2 to 8 columns, titles of 1 to 40 characters and WIP of 1 to 999', () => {
    expect(canAddColumn({ columns: columns.slice(0, 7) })).toBe(true);
    expect(canAddColumn({ columns: [...columns, ...columns] })).toBe(false);
    expect(canRemoveColumn({ columns: columns.slice(0, 2) })).toBe(false);
    expect(canRemoveColumn({ columns: columns.slice(0, 3) })).toBe(true);
    expect(validTitle('  ')).toBe(false);
    expect(validTitle('x'.repeat(40))).toBe(true);
    expect(validTitle('x'.repeat(41))).toBe(false);
    expect(parseWip('')).toBeNull();
    expect(parseWip('5')).toBe(5);
    expect(parseWip('999')).toBe(999);
    expect(parseWip('0')).toBe('invalid');
    expect(parseWip('1000')).toBe('invalid');
    expect(parseWip('2.5')).toBe('invalid');
  });
  it('reloads after stale or vanished state', () => {
    expect(shouldRefetchAfter('tasks.version_conflict')).toBe(true);
    expect(shouldRefetchAfter('tasks.invalid_transition')).toBe(true);
    expect(shouldRefetchAfter('platform.network_error')).toBe(false);
  });
});
