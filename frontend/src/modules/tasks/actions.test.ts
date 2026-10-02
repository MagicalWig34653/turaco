import { describe, expect, it } from 'vitest';
import { createCan } from '../../platform/session/permissions';
import { availableActions, canEditTask, isOverdue, isTerminal } from './actions';
import { taskStatuses, type TaskStatus } from './types';

const can = (...permissions: string[]) => createCan({ permissions });
const actions = (status: TaskStatus, ...permissions: string[]) =>
  availableActions({ status }, can(...permissions));

describe('availableActions', () => {
  it('offers managers every transition the state machine allows', () => {
    expect(actions('open', 'tasks.manage')).toEqual(['start', 'complete', 'block', 'cancel']);
    expect(actions('in_progress', 'tasks.manage')).toEqual(['complete', 'block', 'cancel']);
    expect(actions('blocked', 'tasks.manage')).toEqual(['start', 'unblock', 'cancel']);
    expect(actions('completed', 'tasks.manage')).toEqual(['reopen']);
    expect(actions('cancelled', 'tasks.manage')).toEqual(['reopen']);
  });

  it('keeps cancel and reopen away from workers', () => {
    expect(actions('open', 'tasks.work')).toEqual(['start', 'complete', 'block']);
    expect(actions('blocked', 'tasks.work')).toEqual(['start', 'unblock']);
    expect(actions('completed', 'tasks.work')).toEqual([]);
  });

  it('offers viewers and users without task permissions nothing', () => {
    for (const status of taskStatuses) {
      expect(actions(status, 'tasks.view')).toEqual([]);
      expect(actions(status)).toEqual([]);
    }
  });

  it('never offers a transition out of a terminal state except reopen', () => {
    for (const status of ['completed', 'cancelled'] as const) {
      expect(actions(status, 'tasks.manage', 'tasks.work').filter((a) => a !== 'reopen')).toEqual(
        [],
      );
    }
  });
});

describe('canEditTask', () => {
  it('needs tasks.manage and an unfinished task', () => {
    expect(canEditTask({ status: 'open' }, can('tasks.manage'))).toBe(true);
    expect(canEditTask({ status: 'blocked' }, can('tasks.manage'))).toBe(true);
    expect(canEditTask({ status: 'completed' }, can('tasks.manage'))).toBe(false);
    expect(canEditTask({ status: 'open' }, can('tasks.work', 'tasks.view'))).toBe(false);
  });
});

describe('isOverdue / isTerminal', () => {
  const now = new Date('2026-10-02T12:00:00Z');
  it('flags past due dates of unfinished tasks only', () => {
    expect(isOverdue({ status: 'open', dueAt: '2026-10-01T00:00:00Z' }, now)).toBe(true);
    expect(isOverdue({ status: 'open', dueAt: '2026-10-03T00:00:00Z' }, now)).toBe(false);
    expect(isOverdue({ status: 'open', dueAt: null }, now)).toBe(false);
    expect(isOverdue({ status: 'completed', dueAt: '2026-10-01T00:00:00Z' }, now)).toBe(false);
    expect(isOverdue({ status: 'open', dueAt: 'garbage' }, now)).toBe(false);
  });
  it('knows terminal states', () => {
    expect(isTerminal('completed')).toBe(true);
    expect(isTerminal('cancelled')).toBe(true);
    expect(isTerminal('blocked')).toBe(false);
  });
});
