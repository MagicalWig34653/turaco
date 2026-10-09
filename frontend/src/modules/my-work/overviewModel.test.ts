import { describe, expect, it } from 'vitest';
import type { FeedEntry } from '../briefing/types';
import type { WorkItem } from './api';
import { buildAttention, overviewMetrics } from './overviewModel';

const now = new Date('2026-10-06T12:00:00Z');
const entry = (kind: FeedEntry['kind'], fields: Partial<FeedEntry> = {}): FeedEntry => ({
  kind,
  severity: 'info',
  titleKey: '',
  params: {},
  source: 'briefing',
  linkPath: '/briefing',
  ...fields,
});
const work = (id: string, fields: Partial<WorkItem> = {}): WorkItem => ({
  id,
  source: 'tasks',
  kind: 'task',
  title: id,
  status: 'open',
  priority: 'normal',
  dueAt: null,
  updatedAt: '2026-10-01T00:00:00Z',
  href: `/tasks/${id}`,
  ...fields,
});

describe('overview attention', () => {
  it('orders critical signals, approvals, then pressing work', () => {
    const items = buildAttention(
      [work('normal'), work('urgent', { priority: 'urgent' }), work('high', { priority: 'high' })],
      [entry('pending_approvals', { count: 2 }), entry('major_incident', { severity: 'critical' })],
      now,
    );
    expect(items.map((item) => (item.kind === 'work' ? item.item.id : item.kind))).toEqual([
      'feed',
      'approvals',
      'urgent',
    ]);
  });
  it('skips empty approvals and finished work', () => {
    const items = buildAttention(
      [
        work('done', { status: 'completed', priority: 'urgent' }),
        work('late', { dueAt: '2026-10-01T00:00:00Z' }),
      ],
      [entry('pending_approvals', { count: 0 })],
      now,
    );
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: 'work', overdue: true, tone: 'warning' });
  });
  it('fills with remaining open work without duplicates', () => {
    const items = buildAttention([work('a'), work('b', { priority: 'high' })], [], now);
    expect(items.map((item) => (item.kind === 'work' ? item.item.id : ''))).toEqual(['b', 'a']);
  });
});

describe('overview metrics', () => {
  it('treats tickets like tasks and skips finished ones', () => {
    const items = buildAttention(
      [
        work('t1', { kind: 'ticket', source: 'tickets', priority: 'urgent' }),
        work('t2', { kind: 'ticket', source: 'tickets', status: 'resolved', priority: 'urgent' }),
      ],
      [],
      now,
    );
    expect(items.map((item) => (item.kind === 'work' ? item.item.id : ''))).toEqual(['t1']);
  });
  it('counts open, overdue and alerts and keeps unknown approvals unknown', () => {
    const metrics = overviewMetrics(
      [work('a', { dueAt: '2026-10-01T00:00:00Z' }), work('b', { status: 'cancelled' })],
      [entry('maintenance', { severity: 'warning' }), entry('manual_item')],
      now,
    );
    expect(metrics).toEqual({ open: 1, overdue: 1, approvals: undefined, alerts: 1 });
  });
});
