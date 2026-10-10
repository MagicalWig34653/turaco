import { describe, expect, it } from 'vitest';
import { categoriesIn, filterByCategory, isProbedLink, isTargetGone } from './notificationModel';
import type { AppNotification } from './types';

const n = (id: string, category: string, linkType: string | null = null): AppNotification => ({
  id,
  category,
  params: {},
  linkType,
  linkId: linkType ? 'x' : null,
  createdAt: '2026-10-02T10:00:00Z',
  readAt: null,
});

describe('notification model', () => {
  const items = [n('1', 'task.assigned'), n('2', 'request.approved'), n('3', 'task.assigned')];
  it('lists categories once, sorted', () => {
    expect(categoriesIn(items)).toEqual(['request.approved', 'task.assigned']);
  });
  it('filters by category and keeps all for an empty filter', () => {
    expect(filterByCategory(items, 'task.assigned').map((i) => i.id)).toEqual(['1', '3']);
    expect(filterByCategory(items, '')).toHaveLength(3);
  });
  it('probes ticket links only', () => {
    expect(isProbedLink(n('4', 'x', 'ticket'))).toBe(true);
    expect(isProbedLink(n('5', 'x', 'task'))).toBe(false);
  });
  it('treats only a 404 as a vanished target', () => {
    expect(isTargetGone({ status: 404 })).toBe(true);
    expect(isTargetGone({ status: 500 })).toBe(false);
    expect(isTargetGone(new Error('x'))).toBe(false);
  });
});
