/** Mirrors the Presence OpenAPI contract; no personal free text belongs in these models. */
export type Permission =
  'manageOwn' | 'viewAvailability' | 'viewEntries' | 'manageEntries' | 'manageTeams' | 'admin';
export type PresenceStatus = { enabled: boolean; permissions: Record<Permission, boolean> };
export type Recurrence = {
  frequency: 'daily' | 'weekly' | 'monthly';
  interval: number;
  endsOn: string;
  weekday?: number;
  dayOfMonth?: number;
};
export type Entry = {
  id: string;
  userId: string;
  kind: 'work_location' | 'unavailable';
  locationType: 'location' | 'remote' | 'travelling' | null;
  locationId: string | null;
  startsAt: string;
  endsAt: string;
  allDay: boolean;
  timezone: string | null;
  recurrence: Recurrence | null;
  source: string;
  status: 'active' | 'cancelled';
  visibility: 'availability' | 'detail';
  version: number;
  occurrences: { from: string; to: string }[];
};
export type Availability = {
  userId: string;
  value: 'available' | 'limited' | 'unavailable' | 'unknown';
  explanation: string;
  until?: string | null;
  sources: { source: string; freshness: 'manual' | 'fresh' | 'stale'; observedAt: string | null }[];
};
export type Minimum = {
  teamId: string;
  minimum: number;
  onsiteMinimum: number | null;
  locationId: string | null;
  version: number;
};
export type Coverage = {
  teamId: string;
  members: number | null;
  state: 'ok' | 'below' | 'unknown';
  minimum: Minimum | null;
  days: {
    date: string;
    available: number;
    limited: number;
    unavailable: number;
    unknown: number;
    onsiteAvailable: number | null;
    state: 'ok' | 'below' | 'unknown';
  }[];
};
export type Settings = {
  enabled: boolean;
  retentionDays: number;
  maxRetentionDays: number;
  dpiaRecordedOn: string | null;
  councilConfirmedOn: string | null;
  externalSourcesEnabled: boolean;
  version: number;
};
export type Operation =
  'create' | 'reschedule' | 'change-location' | 'change-recurrence' | 'cancel';
