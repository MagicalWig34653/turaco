import { describe, expect, it } from 'vitest';
import {
  countLabel,
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
    expect(countLabel(counted?.count, counted?.countCapped ?? false)).toBe('7+');
    expect(countLabel(3, false)).toBe('3');
    expect(countLabel(undefined, false)).toBeUndefined();
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
