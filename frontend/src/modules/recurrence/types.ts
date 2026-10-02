// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.
import type { TaskPriority } from '../tasks/types';

export type Frequency = 'daily' | 'weekly' | 'monthly';
export const frequencies: readonly Frequency[] = ['daily', 'weekly', 'monthly'];

export type RecurrenceRule = {
  frequency: Frequency;
  interval: number;
  /** Weekly only; 1 = Monday … 7 = Sunday. */
  weekday: number | null;
  /** Monthly only; clamped to the last day of shorter months. */
  dayOfMonth: number | null;
  /** Local time HH:MM. */
  timeOfDay: string;
  /** IANA time zone name. */
  timezone: string;
  /** Local date YYYY-MM-DD. */
  startsOn: string;
};

export type RecurringTaskDefinition = {
  id: string;
  title: string;
  description: string | null;
  priority: TaskPriority;
  assignedUserId: string | null;
  assignedTeamId: string | null;
  dueAfterHours: number | null;
  rule: RecurrenceRule;
  active: boolean;
  nextRunAt: string | null;
  lastGeneratedAt: string | null;
  createdByUserId: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type DefinitionCreateBody = {
  title: string;
  description?: string;
  priority?: TaskPriority;
  assignedUserId?: string | null;
  assignedTeamId?: string | null;
  dueAfterHours?: number | null;
  rule: RecurrenceRule;
};

export type DefinitionUpdateBody = {
  expectedVersion: number;
  title?: string;
  description?: string;
  priority?: TaskPriority;
  assignedUserId?: string;
  assignedTeamId?: string;
  clearAssignment?: boolean;
  dueAfterHours?: number;
  clearDueAfter?: boolean;
  rule?: RecurrenceRule;
};
