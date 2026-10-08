import type { CanFn } from '../../platform/session/permissions';
import type {
  Availability,
  Entry,
  Operation,
  Permission,
  PresenceStatus,
  Recurrence,
} from './types';
const permissions: Record<string, Permission> = {
  manage_own: 'manageOwn',
  view_availability: 'viewAvailability',
  view_entries: 'viewEntries',
  manage_entries: 'manageEntries',
  manage_teams: 'manageTeams',
  admin: 'admin',
};
export function presenceCan(status: PresenceStatus | undefined, fallback: CanFn): CanFn {
  return (name) => {
    if (!name.startsWith('presence.')) return fallback(name);
    const key = permissions[name.slice(9)];
    return !!(status?.enabled && key && status.permissions[key]);
  };
}
export function dateInZone(
  value: string | Date,
  zone = Intl.DateTimeFormat().resolvedOptions().timeZone,
): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(new Date(value));
  const get = (type: string) => parts.find((p) => p.type === type)!.value;
  return `${get('year')}-${get('month')}-${get('day')}`;
}
export function addDays(date: string, days: number): string {
  const value = new Date(`${date}T12:00:00Z`);
  value.setUTCDate(value.getUTCDate() + days);
  return value.toISOString().slice(0, 10);
}
/** UTC query dates match the API's date-only window semantics, including DST weeks. */
export function calendarWindow(anchor: string, view: 'week' | 'month', now = new Date()) {
  // Backend enforces a rolling 24-hour lookback, so yesterday's midnight is unsafe.
  const floor = now.toISOString().slice(0, 10);
  const first =
    view === 'month'
      ? `${anchor.slice(0, 7)}-01`
      : addDays(anchor, -((new Date(`${anchor}T12:00:00Z`).getUTCDay() + 6) % 7));
  const end =
    view === 'month'
      ? new Date(Date.UTC(Number(anchor.slice(0, 4)), Number(anchor.slice(5, 7)), 1))
          .toISOString()
          .slice(0, 10)
      : addDays(first, 7);
  const from = first < floor ? floor : first;
  const to = end > from ? end : addDays(from, 1);
  const days: string[] = [];
  for (let day = from; day < to; day = addDays(day, 1)) days.push(day);
  return { from, to, days };
}
export function entriesOnDay(entries: Entry[], day: string) {
  // Keep server-expanded occurrences; never independently expand recurrence rules.
  const from = Date.parse(`${day}T00:00:00Z`),
    to = from + 86400000;
  return entries.flatMap((entry) =>
    (entry.occurrences ?? [])
      .filter((o) => Date.parse(o.from) < to && Date.parse(o.to) > from)
      .map((occurrence) => ({ entry, occurrence })),
  );
}
export function entryActions(entry: Entry): Operation[] {
  if (entry.source !== 'manual' || entry.status !== 'active') return [];
  return [
    'reschedule',
    ...(entry.kind === 'work_location' ? ['change-location' as const] : []),
    'change-recurrence',
    'cancel',
  ];
}
export const availabilityTone = (value: Availability['value']) =>
  (
    ({
      available: 'success',
      limited: 'warning',
      unavailable: 'danger',
      unknown: 'neutral',
    }) as const
  )[value] ?? 'neutral';
export type EntryDraft = {
  kind: Entry['kind'];
  locationType: NonNullable<Entry['locationType']>;
  locationId: string;
  allDay: boolean;
  start: string;
  end: string;
  timezone: string;
  visibility: Entry['visibility'];
  frequency: '' | Recurrence['frequency'];
  interval: number;
  endsOn: string;
  weekday: number;
  dayOfMonth: number;
};
export function entryBody(draft: EntryDraft, operation: Operation, entry?: Entry) {
  const version = entry ? { expectedVersion: entry.version } : {};
  if (operation === 'cancel') return version;
  const recurrence = draft.frequency
    ? {
        frequency: draft.frequency,
        interval: draft.interval,
        endsOn: draft.endsOn,
        ...(draft.frequency === 'weekly' ? { weekday: draft.weekday } : {}),
        ...(draft.frequency === 'monthly' ? { dayOfMonth: draft.dayOfMonth } : {}),
      }
    : null;
  if (operation === 'change-recurrence')
    return { ...version, timezone: draft.timezone, recurrence };
  const location = {
    locationType: draft.locationType,
    ...(draft.locationType === 'location' ? { locationId: draft.locationId } : {}),
  };
  if (operation === 'change-location') return { ...version, ...location };
  const times = draft.allDay
    ? { startDate: draft.start, endDate: draft.end, timezone: draft.timezone }
    : {
        startsAt: new Date(draft.start).toISOString(),
        endsAt: new Date(draft.end).toISOString(),
        timezone: draft.timezone,
      };
  if (operation === 'reschedule') return { ...version, ...times };
  return {
    ...times,
    kind: draft.kind,
    visibility: draft.visibility,
    ...(draft.kind === 'work_location' ? location : {}),
    ...(recurrence ? { recurrence } : {}),
  };
}
