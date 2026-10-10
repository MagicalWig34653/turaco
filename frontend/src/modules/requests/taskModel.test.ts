import { describe, expect, it } from 'vitest';
import { canOpenTasks, taskHintKey } from './taskModel';

describe('canOpenTasks', () => {
  it('is true with any task read permission', () => {
    expect(canOpenTasks((p) => p === 'tasks.work')).toBe(true);
    expect(canOpenTasks((p) => p === 'tasks.view')).toBe(true);
  });
  it('is false for a requester without task permissions', () => {
    expect(canOpenTasks(() => false)).toBe(false);
  });
});

describe('taskHintKey', () => {
  it('uses plain wording when the task cannot be opened', () => {
    expect(taskHintKey(true, false)).toBe('requests.task.mandatoryPlain');
    expect(taskHintKey(true, true)).toBe('requests.task.mandatory');
    expect(taskHintKey(false, false)).toBe('requests.task.optional');
  });
});
