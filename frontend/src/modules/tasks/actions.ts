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

/** The Task status each lifecycle action leads to; `unblock` and `reopen` both end in open. */
export const actionTarget: Record<TaskAction, TaskStatus> = {
  start: 'in_progress',
  complete: 'completed',
  block: 'blocked',
  unblock: 'open',
  cancel: 'cancelled',
  reopen: 'open',
};

/**
 * The lifecycle action that takes a task from one status to another, or null when the state
 * machine has none (for example completed to in_progress). Same status needs no action.
 */
export function transitionAction(from: TaskStatus, to: TaskStatus): TaskAction | null {
  if (from === to) return null;
  const candidates = (Object.keys(actionTarget) as TaskAction[]).filter(
    (action) => actionTarget[action] === to && allowedFrom[action].includes(from),
  );
  return candidates[0] ?? null;
}

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

export const resultNoteMaxLength = 1000;

/**
 * A caller whose only task permission is tasks.work reports a task as done explicitly: their
 * completion is a statement to the team, not a tidy-up. Backend authorization is unchanged.
 */
export function needsCompleteConfirmation(can: CanFn): boolean {
  return can('tasks.work') && !can('tasks.manage');
}

/** The note to send: trimmed, or undefined when blank so the optional field stays omitted. */
export function normalizeResultNote(note: string): string | undefined {
  const trimmed = note.trim();
  return trimmed === '' ? undefined : trimmed.slice(0, resultNoteMaxLength);
}
