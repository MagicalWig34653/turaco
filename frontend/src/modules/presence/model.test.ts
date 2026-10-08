import { describe, expect, it } from 'vitest';
import { appRoutes, canViewRoute } from '../../app/routes';
import { navigationCommands } from '../../platform/ui/shell/paletteCommands';
import { shellNavigation } from '../../app/shellNavigation';
import {
  addDays,
  availabilityTone,
  calendarWindow,
  dateInZone,
  entriesOnDay,
  entryActions,
  entryBody,
  presenceCan,
  type EntryDraft,
} from './model';
import type { Entry, PresenceStatus } from './types';
const status: PresenceStatus = {
  enabled: true,
  permissions: {
    manageOwn: true,
    viewAvailability: false,
    viewEntries: false,
    manageEntries: false,
    manageTeams: false,
    admin: false,
  },
};
const entry: Entry = {
  id: 'entry',
  userId: 'user',
  kind: 'work_location',
  locationType: 'remote',
  locationId: null,
  startsAt: '2026-10-25T08:00:00Z',
  endsAt: '2026-10-25T16:00:00Z',
  allDay: false,
  timezone: 'Europe/Berlin',
  recurrence: null,
  source: 'manual',
  status: 'active',
  visibility: 'availability',
  version: 7,
  occurrences: [{ from: '2026-10-25T08:00:00Z', to: '2026-10-25T16:00:00Z' }],
};
const draft: EntryDraft = {
  kind: 'unavailable',
  locationType: 'location',
  locationId: 'hidden-location',
  allDay: true,
  start: '2026-10-25',
  end: '2026-10-26',
  timezone: 'Europe/Berlin',
  visibility: 'availability',
  frequency: 'weekly',
  interval: 2,
  weekday: 1,
  dayOfMonth: 1,
  endsOn: '2026-12-01',
};
describe('Presence visibility across navigation, palette and direct routes', () => {
  it.each([undefined, { ...status, enabled: false }])(
    'fails closed even with session permissions (%s)',
    (value) => {
      const can = presenceCan(value, () => true);
      expect(can('presence.admin')).toBe(false);
      expect(can('presence.manage_own')).toBe(false);
      expect(can('tickets.manage')).toBe(true);
      expect(
        shellNavigation(can)
          .flatMap((group) => group.items)
          .some((route) => route.id.startsWith('presence')),
      ).toBe(false);
      expect(
        navigationCommands(appRoutes, can, (route) => route.titleKey).some((command) =>
          command.id.startsWith('presence'),
        ),
      ).toBe(false);
      for (const route of appRoutes.filter((route) => route.id.startsWith('presence')))
        expect(canViewRoute(can, route)).toBe(false);
    },
  );
  it('uses authoritative status permissions instead of stale session grants', () => {
    const can = presenceCan(status, () => true);
    expect(can('presence.manage_own')).toBe(true);
    expect(can('presence.admin')).toBe(false);
    expect(can('presence.new_permission')).toBe(false);
    expect(
      navigationCommands(appRoutes, can, (route) => route.titleKey)
        .filter((command) => command.id.startsWith('presence'))
        .map((command) => command.id),
    ).toEqual(['presenceMine']);
  });
  it('allows minimum managers without granting availability or admin access', () => {
    const can = presenceCan(
      { ...status, permissions: { ...status.permissions, manageOwn: false, manageTeams: true } },
      () => false,
    );
    expect(
      appRoutes
        .filter((route) => route.id.startsWith('presence') && canViewRoute(can, route))
        .map((route) => route.id),
    ).toEqual(['presenceTeam']);
  });
});
describe('bounded calendars and server occurrences', () => {
  const now = new Date('2026-10-08T18:00:00Z');
  it('clips the current week to today to satisfy the actual rolling 24-hour backend limit', () => {
    expect(calendarWindow('2026-10-08', 'week', now)).toEqual({
      from: '2026-10-08',
      to: '2026-10-12',
      days: ['2026-10-08', '2026-10-09', '2026-10-10', '2026-10-11'],
    });
  });
  it('bounds a complete month to 31 days even across daylight saving changes', () => {
    const window = calendarWindow('2026-10-25', 'month', new Date('2026-09-15T18:00:00Z'));
    expect(window.days).toHaveLength(31);
    expect(Date.parse(window.to) - Date.parse(window.from)).toBe(31 * 86400000);
  });
  it('handles leap years and year rollover', () => {
    expect(calendarWindow('2028-02-20', 'month', now).days).toHaveLength(29);
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01');
    expect(calendarWindow('2026-12-31', 'month', now).to).toBe('2027-01-01');
  });
  it('does not synthesize recurrence occurrences absent from the server response', () => {
    expect(
      entriesOnDay(
        [{ ...entry, recurrence: { frequency: 'daily', interval: 1, endsOn: '2026-10-30' } }],
        '2026-10-26',
      ),
    ).toEqual([]);
  });
  it('uses half-open day overlap for multi-day occurrences', () => {
    const multi = {
      ...entry,
      occurrences: [{ from: '2026-10-24T12:00:00Z', to: '2026-10-26T00:00:00Z' }],
    };
    expect(entriesOnDay([multi], '2026-10-25')).toHaveLength(1);
    expect(entriesOnDay([multi], '2026-10-26')).toHaveLength(0);
  });
  it('preserves a recorded all-day time zone across DST', () => {
    expect(dateInZone('2026-10-24T22:00:00Z', 'Europe/Berlin')).toBe('2026-10-25');
    expect(dateInZone('2026-10-25T23:00:00Z', 'Europe/Berlin')).toBe('2026-10-26');
  });
});
describe('explicit mutation payloads and privacy', () => {
  it('creates unavailable entries without leaking old location data or personal free text', () => {
    const body = entryBody(draft, 'create');
    expect(body).toEqual({
      startDate: '2026-10-25',
      endDate: '2026-10-26',
      timezone: 'Europe/Berlin',
      kind: 'unavailable',
      visibility: 'availability',
      recurrence: { frequency: 'weekly', interval: 2, endsOn: '2026-12-01', weekday: 1 },
    });
    expect(body).not.toHaveProperty('expectedVersion');
    expect(body).not.toHaveProperty('reason');
  });
  it.each(['reschedule', 'change-location', 'change-recurrence', 'cancel'] as const)(
    'binds %s to the displayed version',
    (operation) => {
      expect(entryBody(draft, operation, entry)).toHaveProperty('expectedVersion', 7);
    },
  );
  it('sends only expectedVersion for cancellation', () => {
    expect(entryBody(draft, 'cancel', entry)).toEqual({ expectedVersion: 7 });
  });
  it('removes recurrence explicitly with null', () => {
    expect(entryBody({ ...draft, frequency: '' }, 'change-recurrence', entry)).toEqual({
      expectedVersion: 7,
      timezone: 'Europe/Berlin',
      recurrence: null,
    });
  });
  it('omits irrelevant location ids and recurrence fields when changing remote location', () => {
    expect(entryBody({ ...draft, locationType: 'remote' }, 'change-location', entry)).toEqual({
      expectedVersion: 7,
      locationType: 'remote',
    });
  });
  it('sends timed instants without all-day fields', () => {
    expect(
      entryBody(
        {
          ...draft,
          allDay: false,
          start: '2026-10-25T09:00:00+01:00',
          end: '2026-10-25T17:00:00+01:00',
        },
        'reschedule',
        entry,
      ),
    ).toEqual({
      expectedVersion: 7,
      startsAt: '2026-10-25T08:00:00.000Z',
      endsAt: '2026-10-25T16:00:00.000Z',
      timezone: 'Europe/Berlin',
    });
  });
  it('offers no mutations for external or cancelled entries and no location change for unavailable entries', () => {
    expect(entryActions({ ...entry, source: 'm365' })).toEqual([]);
    expect(entryActions({ ...entry, status: 'cancelled' })).toEqual([]);
    expect(entryActions({ ...entry, kind: 'unavailable' })).not.toContain('change-location');
  });
  it('keeps unknown neutral, never available', () => {
    expect(availabilityTone('unknown')).toBe('neutral');
    expect(availabilityTone('available')).toBe('success');
    expect(availabilityTone('unavailable')).toBe('danger');
    expect(availabilityTone('limited')).toBe('warning');
  });
});
