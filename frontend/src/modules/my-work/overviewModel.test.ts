import { describe, expect, it } from 'vitest';
import type { FeedEntry } from '../briefing/types';
import type { Task } from '../tasks/types';
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
const task = (id: string, fields: Partial<Task> = {}): Task => ({
  id,
  title: id,
  description: null,
  status: 'open',
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
  version: 1,
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-01T00:00:00Z',
  ...fields,
});

describe('overview attention', () => {
  it('orders critical signals, approvals, then pressing work', () => {
    const items = buildAttention(
      [task('normal'), task('urgent', { priority: 'urgent' }), task('high', { priority: 'high' })],
      [entry('pending_approvals', { count: 2 }), entry('major_incident', { severity: 'critical' })],
      now,
    );
    expect(items.map((item) => (item.kind === 'task' ? item.task.id : item.kind))).toEqual([
      'feed',
      'approvals',
      'urgent',
    ]);
  });
  it('skips empty approvals and finished work', () => {
    const items = buildAttention(
      [
        task('done', { status: 'completed', priority: 'urgent' }),
        task('late', { dueAt: '2026-10-01T00:00:00Z' }),
      ],
      [entry('pending_approvals', { count: 0 })],
      now,
    );
    expect(items).toHaveLength(1);
    expect(items[0]).toMatchObject({ kind: 'task', overdue: true, tone: 'warning' });
  });
  it('fills with remaining open work without duplicates', () => {
    const items = buildAttention([task('a'), task('b', { priority: 'high' })], [], now);
    expect(items.map((item) => (item.kind === 'task' ? item.task.id : ''))).toEqual(['b', 'a']);
  });
});

describe('overview metrics', () => {
  it('counts open, overdue and alerts and keeps unknown approvals unknown', () => {
    const metrics = overviewMetrics(
      [task('a', { dueAt: '2026-10-01T00:00:00Z' }), task('b', { status: 'cancelled' })],
      [entry('maintenance', { severity: 'warning' }), entry('manual_item')],
      now,
    );
    expect(metrics).toEqual({ open: 1, overdue: 1, approvals: undefined, alerts: 1 });
  });
});
