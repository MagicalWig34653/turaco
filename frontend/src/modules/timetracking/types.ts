// Types mirror api/openapi/openapi.yaml (Time Tracking, F17 T-A).
export type TimeEntry = {
  id: string;
  subjectType: 'ticket';
  subjectId: string;
  /** Ticket number; only in the own-time list and only while the caller may view the Ticket. */
  reference?: string;
  userId?: string;
  userName?: string;
  own: boolean;
  editable: boolean;
  workDate: string;
  durationMinutes: number;
  source: 'manual' | 'timer';
  note: string | null;
  createdAt: string;
  updatedAt: string;
  version: number;
};

export type TimeEntryList = {
  items: TimeEntry[];
  totalMinutes: number;
  othersMinutes?: number;
  canLog?: boolean;
  showEntryAuthors?: boolean;
  editWindowDays: number;
};

export type TimeTrackingSettings = {
  codeterminationBasis: 'agreement' | 'not_applicable' | null;
  codeterminationRecordedOn: string | null;
  dpiaRecordedOn: string | null;
  editWindowDays: number;
  retentionMonths: number;
  showEntryAuthors: boolean;
  minReportGroupSize: number;
  version: number;
  updatedAt: string;
};

export type TimeTrackingSettingsRequest = {
  codeterminationBasis?: 'agreement' | 'not_applicable';
  codeterminationRecordedOn?: string;
  dpiaRecordedOn?: string;
  editWindowDays: number;
  retentionMonths: number;
  showEntryAuthors: boolean;
  minReportGroupSize: number;
  expectedVersion: number;
};

export const deleteReasons = ['wrong_ticket', 'duplicate', 'wrong_duration', 'other'] as const;
export type DeleteReason = (typeof deleteReasons)[number];

/** The signed-in person's running timer (personal state). */
export type RunningTimer = {
  subjectType: 'ticket';
  subjectId: string;
  startedAt: string;
  note: string | null;
};

export type EffortGroup = {
  key: string;
  label?: string;
  suppressed: boolean;
  minutes?: number;
  entries?: number;
  contributors?: number;
};

export type EffortReport = {
  from: string;
  to: string;
  groupBy: 'queue' | 'month' | 'ticket';
  minGroupSize: number;
  groups: EffortGroup[];
  truncated: boolean;
};
