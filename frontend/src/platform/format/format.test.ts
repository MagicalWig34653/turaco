import { describe, expect, it } from 'vitest';
import {
  formatJson,
  groupPermissions,
  isoToLocalInput,
  isValidRoleKey,
  localInputToIso,
  permissionPrefix,
  retryAfterMinutes,
  summarizeSyncCounts,
} from './format';

describe('retryAfterMinutes', () => {
  it('rounds up and never returns less than one minute', () => {
    expect(retryAfterMinutes(1)).toBe(1);
    expect(retryAfterMinutes(60)).toBe(1);
    expect(retryAfterMinutes(61)).toBe(2);
    expect(retryAfterMinutes(900)).toBe(15);
    expect(retryAfterMinutes(undefined)).toBe(1);
    expect(retryAfterMinutes(0)).toBe(1);
  });
});

describe('groupPermissions', () => {
  it('groups by prefix and sorts groups and items', () => {
    const groups = groupPermissions(
      ['platform.roles.view', 'organization.view', 'platform.roles.manage', 'platform.audit.view'],
      (name) => name,
    );
    expect(groups.map((group) => group.prefix)).toEqual([
      'organization',
      'platform.audit',
      'platform.roles',
    ]);
    expect(groups[2]?.items).toEqual(['platform.roles.manage', 'platform.roles.view']);
    expect(permissionPrefix('single')).toBe('single');
  });
});

describe('isValidRoleKey', () => {
  it('follows the API pattern', () => {
    expect(isValidRoleKey('it-admin')).toBe(true);
    expect(isValidRoleKey('a')).toBe(false);
    expect(isValidRoleKey('-ab')).toBe(false);
    expect(isValidRoleKey('Ab')).toBe(false);
    expect(isValidRoleKey('a'.repeat(64))).toBe(false);
  });
});

describe('localInputToIso', () => {
  it('returns undefined for empty or invalid input and RFC 3339 otherwise', () => {
    expect(localInputToIso('')).toBeUndefined();
    expect(localInputToIso('nope')).toBeUndefined();
    expect(localInputToIso('2026-01-02T03:04')).toMatch(
      /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/,
    );
  });
});

describe('formatJson', () => {
  it('pretty prints and renders undefined as null', () => {
    expect(formatJson({ a: 1 })).toBe('{\n  "a": 1\n}');
    expect(formatJson(undefined)).toBe('null');
  });
});

describe('summarizeSyncCounts', () => {
  it('is null for runs without counts and defaults missing counters', () => {
    expect(summarizeSyncCounts({})).toBeNull();
    expect(summarizeSyncCounts({ usersObserved: 5 })).toEqual({
      usersObserved: 5,
      usersCreated: 0,
      usersUpdated: 0,
      groupsObserved: 0,
    });
  });
});

describe('isoToLocalInput', () => {
  it('round-trips through localInputToIso at minute precision', () => {
    const iso = '2026-11-01T09:30:00.000Z';
    const local = isoToLocalInput(iso);
    expect(local).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/);
    expect(localInputToIso(local)).toBe(iso);
  });

  it('is empty for missing or invalid values', () => {
    expect(isoToLocalInput(null)).toBe('');
    expect(isoToLocalInput(undefined)).toBe('');
    expect(isoToLocalInput('')).toBe('');
    expect(isoToLocalInput('garbage')).toBe('');
  });
});
