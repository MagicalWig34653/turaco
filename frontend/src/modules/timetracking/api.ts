import { api } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import type {
  DeleteReason,
  RunningTimer,
  TimeEntry,
  TimeEntryList,
  TimeTrackingSettings,
  TimeTrackingSettingsRequest,
} from './types';

registerErrorMessages({
  'timetracking.invalid_request': 'error.invalidRequest',
  'timetracking.not_permitted': 'error.forbidden',
  'timetracking.not_found': 'error.notFound',
  'timetracking.version_conflict': 'timetracking.error.conflict',
  'timetracking.outside_edit_window': 'timetracking.error.outsideWindow',
  'timetracking.day_limit': 'timetracking.error.dayLimit',
  'timetracking.timer_running': 'timetracking.error.timerRunning',
});

const base = '/time-tracking';

export type EntryInput = {
  subjectId: string;
  workDate: string;
  durationMinutes: number;
  note: string;
  reason?: string;
};

export const timeTrackingApi = {
  /** GET /time-tracking/entries?subjectType=ticket: the Ticket total and, for workers, the entries. */
  forTicket: (ticketId: string, signal?: AbortSignal) =>
    api.get<TimeEntryList>(`${base}/entries`, {
      query: { subjectType: 'ticket', subjectId: ticketId },
      signal,
    }),
  /** GET /time-tracking/entries?mine=true: the caller's own time between two work dates. */
  mine: (from: string, to: string, signal?: AbortSignal) =>
    api.get<TimeEntryList>(`${base}/entries`, { query: { mine: true, from, to }, signal }),
  create: (input: EntryInput) =>
    api.post<TimeEntry>(`${base}/entries`, { subjectType: 'ticket', ...input }),
  update: (id: string, input: EntryInput, expectedVersion: number) =>
    api.put<TimeEntry>(`${base}/entries/${encodeURIComponent(id)}`, { ...input, expectedVersion }),
  remove: (id: string, deleteReason: DeleteReason, expectedVersion: number, reason?: string) =>
    api.post<void>(`${base}/entries/${encodeURIComponent(id)}/delete`, {
      deleteReason,
      expectedVersion,
      ...(reason ? { reason } : {}),
    }),
  /** GET /time-tracking/timer: the caller's own running timer or null. */
  timer: (signal?: AbortSignal) =>
    api.get<{ timer: RunningTimer | null }>(`${base}/timer`, { signal }),
  startTimer: (ticketId: string, note?: string) =>
    api.post<RunningTimer>(`${base}/timer`, {
      subjectType: 'ticket',
      subjectId: ticketId,
      ...(note ? { note } : {}),
    }),
  stopTimer: (durationMinutes?: number) =>
    api.post<TimeEntry>(`${base}/timer/stop`, durationMinutes ? { durationMinutes } : {}),
  discardTimer: () => api.delete<void>(`${base}/timer`),
  settings: (signal?: AbortSignal) => api.get<TimeTrackingSettings>(`${base}/settings`, { signal }),
  saveSettings: (body: TimeTrackingSettingsRequest) =>
    api.put<TimeTrackingSettings>(`${base}/settings`, body),
};
