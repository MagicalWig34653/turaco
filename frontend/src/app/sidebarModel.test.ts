import { describe, expect, it } from 'vitest';
import type { SidebarResponse } from '../platform/ui/views/api';
import {
  applyCounts,
  countIds,
  countLabel,
  isEditablePin,
  isPinnedActive,
  pinnedActiveOn,
  pinnedItems,
  sanitizeCollapsed,
  toggleSection,
} from './sidebarModel';

const pin = (id: string, groupKey: string, position: number, extra = {}) => ({
  viewId: id,
  name: id,
  resource: groupKey === 'endpoints' ? 'devices' : groupKey,
  groupKey,
  position,
  hidden: false,
  source: 'user' as const,
  ...extra,
});

describe('collapsed sections', () => {
  it('keeps only well-formed unique ids', () => {
    expect(sanitizeCollapsed(['work', 'work', 'Bad Id', 5, 'service_desk', ''])).toEqual([
      'work',
      'service_desk',
    ]);
    expect(sanitizeCollapsed('work')).toEqual([]);
    expect(sanitizeCollapsed(Array.from({ length: 40 }, (_, i) => `s${i}`))).toHaveLength(20);
  });
  it('toggles a section', () => {
    expect(toggleSection(['work'], 'admin')).toEqual(['work', 'admin']);
    expect(toggleSection(['work', 'admin'], 'work')).toEqual(['admin']);
  });
});

describe('pinned items', () => {
  const sidebar = {
    groups: [
      { key: 'tasks', items: [pin('t2', 'tasks', 1), pin('t1', 'tasks', 0)] },
      {
        key: 'tickets',
        items: [pin('k1', 'tickets', 0), pin('hid', 'tickets', 1, { hidden: true })],
      },
      { key: 'endpoints', items: [pin('d1', 'endpoints', 0)] },
    ],
  };
  it('orders by group, then position, and drops hidden pins', () => {
    expect(pinnedItems(sidebar).map((item) => item.viewId)).toEqual(['k1', 't1', 't2', 'd1']);
  });
  it('links to the resource list with the view id', () => {
    const items = pinnedItems(sidebar);
    expect(items.find((item) => item.viewId === 'd1')?.href).toBe('/devices?view=d1');
    expect(items.find((item) => item.viewId === 'k1')?.href).toBe('/service-desk?view=k1');
  });
  it('is empty without a sidebar response', () => {
    expect(pinnedItems(undefined)).toEqual([]);
  });
  it('carries counts only when the backend sends them', () => {
    const [plain] = pinnedItems({ groups: [{ key: 'tickets', items: [pin('a', 'tickets', 0)] }] });
    expect(plain?.count).toBeUndefined();
    const [counted] = pinnedItems({
      groups: [
        { key: 'tickets', items: [pin('a', 'tickets', 0, { count: 7, countCapped: true })] },
      ],
    });
    expect(countLabel(counted?.count, counted?.countCapped ?? false)).toBe('99+');
    expect(countLabel(3, false)).toBe('3');
    expect(countLabel(99, false)).toBe('99');
    expect(countLabel(100, false)).toBe('99+');
    expect(countLabel(0, false)).toBe('0');
    expect(countLabel(undefined, false)).toBeUndefined();
  });
  it('shows a dash for an unavailable count and never zero', () => {
    expect(countLabel(undefined, false, 'unavailable')).toBe('–');
    expect(countLabel(0, false, 'unavailable')).toBe('–');
    const [entry] = pinnedItems({
      groups: [
        {
          key: 'tickets',
          items: [
            pin('s', 'tickets', 0, { source: 'system', countStatus: 'unavailable', count: 0 }),
          ],
        },
      ],
    });
    expect(entry?.count).toBeUndefined();
    expect(countLabel(entry?.count, entry?.countCapped ?? false, entry?.countStatus)).toBe('–');
  });
  it('puts System Views before the user pins of their group and keeps them fixed', () => {
    const items = pinnedItems({
      groups: [
        {
          key: 'tickets',
          items: [
            pin('mine', 'tickets', 0),
            pin('system:tickets:my-open', 'tickets', 5, {
              source: 'system',
              name: '',
              nameKey: 'views.system.my_open_tickets',
            }),
          ],
        },
      ],
    });
    expect(items.map((item) => item.viewId)).toEqual(['system:tickets:my-open', 'mine']);
    expect(items[0]?.nameKey).toBe('views.system.my_open_tickets');
    expect(items[0] && isEditablePin(items[0])).toBe(false);
    expect(items[1] && isEditablePin(items[1])).toBe(true);
  });
  it('links a System View with its key in the view parameter', () => {
    const [item] = pinnedItems({
      groups: [
        {
          key: 'tickets',
          items: [pin('system:tickets:queue:q1', 'tickets', 0, { source: 'system' })],
        },
      ],
    });
    expect(item?.href).toBe('/service-desk?view=system%3Atickets%3Aqueue%3Aq1');
  });
  it('refreshes counts by id and drops those the server no longer returns', () => {
    const sidebar: SidebarResponse = {
      collapsedGroups: [],
      groups: [
        {
          key: 'tickets',
          items: [
            pin('a', 'tickets', 0, { count: 1 }),
            pin('b', 'tickets', 1, { count: 2 }),
            pin('c', 'tickets', 2, { count: 3 }),
            pin('d', 'tickets', 3),
          ],
        },
      ],
    };
    const ids = countIds(pinnedItems(sidebar));
    expect(ids).toEqual(['a', 'b', 'c']);
    const next = applyCounts(
      sidebar,
      [
        { id: 'a', count: 5, capped: false, status: 'ok' },
        { id: 'b', status: 'unavailable' },
      ],
      new Set(ids),
    );
    const byId = Object.fromEntries(
      next.groups[0]?.items.map((entry) => [entry.viewId, entry]) ?? [],
    );
    expect(byId.a).toMatchObject({ count: 5, countStatus: 'ok' });
    expect(byId.b?.count).toBeUndefined();
    expect(byId.b?.countStatus).toBe('unavailable');
    expect(byId.c?.count).toBeUndefined();
    expect(byId.d?.count).toBeUndefined();
    expect(sidebar.groups[0]?.items[0]?.count).toBe(1);
  });
  it('highlights a pin only for its own view on its route', () => {
    const [item] = pinnedItems({ groups: [{ key: 'tasks', items: [pin('t1', 'tasks', 0)] }] });
    if (!item) throw new Error('missing item');
    expect(isPinnedActive(item, '/tasks', '?view=t1&x=1')).toBe(true);
    expect(isPinnedActive(item, '/tasks', '?view=other')).toBe(false);
    expect(isPinnedActive(item, '/devices', '?view=t1')).toBe(false);
    expect(pinnedActiveOn([item], '/tasks', '?view=t1')).toBe(item);
    expect(pinnedActiveOn([item], '/tasks', '')).toBeUndefined();
  });
});

describe('pinned Task Boards', () => {
  const sidebar = {
    groups: [
      {
        key: 'tasks',
        items: [
          pin('view-1', 'tasks', 0, { kind: 'board' as const, ref: 'board 1', name: 'Sprint' }),
          pin('view-2', 'tasks', 1),
        ],
      },
    ],
  };
  it('links the Board screen for a board pin and the list for any other View', () => {
    const items = pinnedItems(sidebar);
    expect(items[0]?.href).toBe('/tasks/boards/board%201');
    expect(items[1]?.href).toBe('/tasks?view=view-2');
  });
  it('is active on the Board path without a view parameter', () => {
    const [board] = pinnedItems(sidebar);
    expect(board && isPinnedActive(board, '/tasks/boards/board%201', '')).toBe(true);
    expect(board && isPinnedActive(board, '/tasks/boards/other', '')).toBe(false);
    expect(board && isPinnedActive(board, '/tasks', '?view=view-1')).toBe(false);
  });
});
