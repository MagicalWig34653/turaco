import { describe, expect, it } from 'vitest';
import type { WorkItem } from './api';
import {
  countText,
  countViews,
  dedupeItems,
  figureOf,
  isItemOverdue,
  parseSourceFilter,
  priorityOf,
  sourceLabelKey,
  sourcesFor,
  totalOf,
} from './feedModel';

const item = (id: string, fields: Partial<WorkItem> = {}): WorkItem => ({
  id,
  source: 'tickets',
  kind: 'ticket',
  title: id,
  status: 'open',
  priority: 'normal',
  dueAt: null,
  updatedAt: '2026-10-01T00:00:00Z',
  href: `/support/${id}`,
  ...fields,
});

describe('source filter', () => {
  it('accepts known sources only and falls back to all', () => {
    expect(parseSourceFilter('team_tickets')).toBe('team_tickets');
    expect(parseSourceFilter('tasks')).toBe('tasks');
    expect(parseSourceFilter('approvals')).toBe('all');
    expect(parseSourceFilter(null)).toBe('all');
  });
  it('requests every source for all and one source otherwise', () => {
    expect(sourcesFor('all')).toBeUndefined();
    expect(sourcesFor('tickets')).toEqual(['tickets']);
  });
  it('labels unknown sources generically', () => {
    expect(sourceLabelKey('tasks')).toBe('myWork.source.tasks');
    expect(sourceLabelKey('approvals')).toBe('myWork.source.unknown');
  });
});

describe('merged feed items', () => {
  it('drops repeated items of the same source but keeps equal ids of different sources', () => {
    const result = dedupeItems([
      item('1'),
      item('1'),
      item('1', { source: 'team_tickets' }),
      item('2'),
    ]);
    expect(result.map((entry) => `${entry.source}:${entry.id}`)).toEqual([
      'tickets:1',
      'team_tickets:1',
      'tickets:2',
    ]);
  });
  it('reads overdue only for unfinished items with a past due date', () => {
    const now = new Date('2026-10-09T12:00:00Z');
    expect(isItemOverdue(item('a', { dueAt: '2026-10-01T00:00:00Z' }), now)).toBe(true);
    expect(isItemOverdue(item('b', { dueAt: '2026-10-20T00:00:00Z' }), now)).toBe(false);
    expect(isItemOverdue(item('c', { dueAt: null }), now)).toBe(false);
    expect(
      isItemOverdue(item('d', { dueAt: '2026-10-01T00:00:00Z', status: 'resolved' }), now),
    ).toBe(false);
    expect(isItemOverdue(item('e', { dueAt: 'garbage' }), now)).toBe(false);
  });
  it('maps unknown priorities to normal', () => {
    expect(priorityOf({ priority: 'urgent' })).toBe('urgent');
    expect(priorityOf({ priority: 'whatever' })).toBe('normal');
  });
});

describe('source counts', () => {
  const counts = countViews([
    { source: 'tickets', status: 'ok', count: 4 },
    { source: 'team_tickets', status: 'ok', count: 1000, capped: true },
    { source: 'tasks', status: 'unavailable' },
  ]);
  it('never turns an unavailable count into zero', () => {
    expect(counts.get('tasks')).toMatchObject({ count: undefined, unavailable: true });
    expect(countText(counts.get('tasks'))).toBe('–');
    expect(countText(undefined)).toBe('–');
  });
  it('marks capped counts', () => {
    expect(countText(counts.get('team_tickets'))).toBe('1000+');
    expect(countText(counts.get('tickets'))).toBe('4');
  });
  it('sums the available sources and flags a partial or unknown total', () => {
    expect(totalOf(counts)).toEqual({ value: 1004, capped: true, partial: true, unknown: false });
    expect(totalOf(countViews([{ source: 'tasks', status: 'unavailable' }]))).toMatchObject({
      unknown: true,
      partial: true,
    });
    expect(totalOf(countViews([]))).toMatchObject({ unknown: true, partial: false });
    expect(totalOf(countViews([{ source: 'tasks', status: 'ok', count: 0 }]))).toEqual({
      value: 0,
      capped: false,
      partial: false,
      unknown: false,
    });
  });
});

describe('figureOf', () => {
  const counts = countViews([
    { source: 'tickets', count: 2, status: 'ok' },
    { source: 'tasks', count: 3, capped: true, status: 'ok' },
    { source: 'team_tickets', status: 'unavailable' },
  ]);
  it('sums the selected sources and keeps them apart', () => {
    expect(figureOf(counts, ['tickets', 'tasks'])).toMatchObject({
      value: 5,
      capped: true,
      partial: false,
      unknown: false,
    });
  });
  it('never turns an unavailable source into zero', () => {
    expect(figureOf(counts, ['team_tickets'])).toMatchObject({ unknown: true, partial: true });
    expect(figureOf(counts, ['tickets', 'team_tickets'])).toMatchObject({
      value: 2,
      partial: true,
      unknown: false,
    });
  });
  it('marks sources the caller does not have as absent', () => {
    expect(figureOf(new Map(), ['tickets']).absent).toBe(true);
  });
});
