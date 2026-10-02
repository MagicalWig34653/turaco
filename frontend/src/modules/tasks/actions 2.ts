import type { CanFn } from '../../platform/session/permissions';
import type { Task, TaskAction, TaskStatus } from './types';

/** Mirrors the Task state machine (docs/domain/state-machines.md); the backend enforces it. */
const allowedFrom: Record<TaskAction, readonly TaskStatus[]> = {
  start: ['open', 'blocked'],
  complete: ['open', 'in_progress'],
  block: ['open', 'in_progress'],
  unblock: ['blocked'],
  cancel: ['open', 'in_progress', 'blocked'],
  reopen: ['completed', 'cancelled'],
};

const actionOrder: readonly TaskAction[] = [
  'start',
  'complete',
  'block',
  'unblock',
  'cancel',
  'reopen',
];

/** Actions that need tasks.manage; the others also work with tasks.work on one's own tasks. */
const managerOnly: ReadonlySet<TaskAction> = new Set(['cancel', 'reopen']);

/** Actions that require a reason. */
export const reasonRequired: ReadonlySet<TaskAction> = new Set(['block', 'cancel', 'reopen']);

export function isTerminal(status: TaskStatus): boolean {
  return status === 'completed' || status === 'cancelled';
}

/**
 * Lifecycle actions to offer for a task. UI hiding only: a worker may see an action for a task that
 * is not assigned to them and gets a 403 from the backend.
 */
export function availableActions(task: Pick<Task, 'status'>, can: CanFn): TaskAction[] {
  const manage = can('tasks.manage');
  const work = can('tasks.work');
  return actionOrder.filter((action) => {
    if (!allowedFrom[action].includes(task.status)) return false;
    return manage || (work && !managerOnly.has(action));
  });
}

/** Editing details and assignment need tasks.manage and an unfinished task. */
export function canEditTask(task: Pick<Task, 'status'>, can: CanFn): boolean {
  return can('tasks.manage') && !isTerminal(task.status);
}

export function isOverdue(task: Pick<Task, 'status' | 'dueAt'>, now: Date): boolean {
  if (!task.dueAt || isTerminal(task.status)) return false;
  const due = new Date(task.dueAt);
  return !Number.isNaN(due.getTime()) && due.getTime() < now.getTime();
}
