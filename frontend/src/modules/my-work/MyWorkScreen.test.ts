import { describe, expect, it } from 'vitest';
import { prioritizeWork } from './MyWorkScreen';
import type { Task } from '../tasks/types';

const task = (id: string, priority: Task['priority'], dueAt: string | null): Task => ({
  id,
  title: id,
  description: null,
  status: 'open',
  statusReason: null,
  priority,
  assignedUserId: null,
  assignedUserName: null,
  assignedTeamId: null,
  assignedTeamName: null,
  contextType: null,
  contextId: null,
  dueAt,
  completedAt: null,
  createdByUserId: null,
  completedByUserId: null,
  version: 1,
  createdAt: '',
  updatedAt: '',
});

describe('My Work priority order', () => {
  it('puts urgent obligations first, then earlier due dates within each priority', () => {
    const items = [
      task('low', 'low', null),
      task('high-later', 'high', '2026-10-09'),
      task('urgent', 'urgent', null),
      task('high-sooner', 'high', '2026-10-08'),
    ];
    expect(prioritizeWork(items).map((item) => item.id)).toEqual([
      'urgent',
      'high-sooner',
      'high-later',
      'low',
    ]);
    expect(items[0]?.id).toBe('low');
  });
});
