import { describe, expect, it } from 'vitest';
import {
  diffPermissions,
  endOfDayIso,
  expiryState,
  filterEffective,
  filterPermissions,
  grantReasons,
  grantability,
  groupByModule,
  missingNeeds,
  moduleOf,
  proposeRoleKey,
  riskCounts,
  sodConflicts,
  validateExpiry,
} from './accessModel';
import type { EffectivePermissions, Permission } from './types';

const perm = (name: string, extra: Partial<Permission> = {}): Permission => ({
  name,
  description: `${name} description`,
  risk: 'normal',
  ...extra,
});
const catalog: Permission[] = [
  perm('tickets.view', { module: 'servicedesk', group: 'view' }),
  perm('tickets.manage', { module: 'servicedesk', group: 'manage', risk: 'elevated' }),
  perm('assets.view', { module: 'assets', group: 'view' }),
  perm('remote_access.start_attended', {
    module: 'remoteaccess',
    group: 'other',
    risk: 'high',
    needs: ['assets.view', 'remote_access.view'],
  }),
  perm('remote_access.view', { module: 'remoteaccess', group: 'view' }),
];
const byName = new Map(catalog.map((p) => [p.name, p]));

describe('grouping and filtering', () => {
  it('groups by module and then by permission group', () => {
    const buckets = groupByModule(catalog);
    expect(buckets.map((b) => b.module)).toEqual(['assets', 'remoteaccess', 'servicedesk']);
    const desk = buckets.find((b) => b.module === 'servicedesk');
    expect(desk?.groups.map((g) => g.group)).toEqual(['view', 'manage']);
  });
  it('falls back to the first name segment without a module', () => {
    expect(moduleOf({ name: 'orphans.view' })).toBe('orphans');
  });
  it('filters by search text, risk and selection', () => {
    const selected = new Set(['assets.view']);
    expect(
      filterPermissions(catalog, { search: 'TICKETS', risk: '', onlySelected: false }, selected),
    ).toHaveLength(2);
    expect(
      filterPermissions(catalog, { search: '', risk: 'high', onlySelected: false }, selected),
    ).toHaveLength(1);
    expect(
      filterPermissions(catalog, { search: '', risk: '', onlySelected: true }, selected).map(
        (p) => p.name,
      ),
    ).toEqual(['assets.view']);
  });
  it('counts risk classes', () => {
    expect(riskCounts(['tickets.manage', 'remote_access.start_attended', 'nope'], byName)).toEqual({
      normal: 0,
      elevated: 1,
      high: 1,
    });
  });
});

describe('needs and ceilings', () => {
  it('lists missing companion permissions', () => {
    const selected = new Set(['remote_access.start_attended', 'assets.view']);
    expect(missingNeeds(selected, byName)).toEqual([
      { permission: 'remote_access.start_attended', needs: ['remote_access.view'] },
    ]);
    expect(missingNeeds(new Set(['assets.view']), byName)).toEqual([]);
  });
  it('explains what the actor may grant', () => {
    const actor = { isAdministrator: false, has: (name: string) => name === 'tickets.view' };
    expect(grantability(perm('tickets.view'), actor)).toBe('ok');
    expect(grantability(perm('assets.view'), actor)).toBe('beyondOwn');
    expect(grantability(perm('x', { risk: 'high' }), actor)).toBe('administratorOnly');
    expect(
      grantability(perm('x', { risk: 'high' }), { isAdministrator: true, has: () => false }),
    ).toBe('ok');
  });
});

describe('diff and separation of duties', () => {
  it('computes added and removed permissions', () => {
    expect(diffPermissions(['a', 'b'], ['b', 'c'])).toEqual({ added: ['c'], removed: ['a'] });
  });
  it('detects rules with a permission on both sides', () => {
    const rules = [
      { key: 'r', messageKey: 'm', left: ['changes.manage'], right: ['changes.approve'] },
    ];
    expect(sodConflicts(new Set(['changes.manage']), rules)).toEqual([]);
    expect(sodConflicts(new Set(['changes.manage', 'changes.approve']), rules)).toHaveLength(1);
  });
});

describe('expiry', () => {
  const now = Date.parse('2026-10-10T12:00:00Z');
  const role = { builtIn: false, key: 'custom', permissions: ['tickets.view'] };
  it('classifies stored expiry', () => {
    expect(expiryState(undefined, now)).toBe('none');
    expect(expiryState('2026-10-09T00:00:00Z', now)).toBe('expired');
    expect(expiryState('2026-10-15T00:00:00Z', now)).toBe('soon');
    expect(expiryState('2027-03-01T00:00:00Z', now)).toBe('active');
  });
  it('validates a chosen end date', () => {
    expect(validateExpiry('', role, byName, now)).toBeNull();
    expect(validateExpiry('2026-10-09', role, byName, now)).toBe('past');
    expect(validateExpiry('2026-12-31', role, byName, now)).toBeNull();
    expect(
      validateExpiry('2026-12-31', { ...role, key: 'platform-administrator' }, byName, now),
    ).toBe('administratorNoExpiry');
    const risky = { ...role, permissions: ['remote_access.start_attended'] };
    expect(validateExpiry('2028-01-01', risky, byName, now)).toBe('tooFar');
    expect(validateExpiry('2027-01-01', risky, byName, now)).toBeNull();
  });
  it('turns a calendar day into an instant', () => {
    expect(endOfDayIso('2026-10-10')).toMatch(/^2026-10-1\dT/);
    expect(endOfDayIso('nonsense')).toBeUndefined();
  });
});

describe('role keys', () => {
  it('proposes a valid key from a name', () => {
    expect(proposeRoleKey('Erste Hilfe – Süd!')).toBe('erste-hilfe-sud');
    expect(proposeRoleKey('  ')).toBe('');
    expect(proposeRoleKey('Straße')).toBe('strasse');
  });
});

describe('effective permissions', () => {
  const effective: EffectivePermissions = {
    userId: 'u',
    roles: [
      {
        roleId: 'r1',
        roleKey: 'a',
        roleName: 'A',
        builtInAdmin: false,
        assignmentId: 'as1',
        source: 'direct',
      },
      {
        roleId: 'r2',
        roleKey: 'b',
        roleName: 'B',
        builtInAdmin: false,
        assignmentId: 'as2',
        source: 'directory_group',
        groupId: 'g',
      },
    ],
    permissions: [
      { name: 'tickets.view', risk: 'normal', module: 'servicedesk', grantedBy: ['as1', 'as2'] },
      { name: 'tickets.manage', risk: 'elevated', module: 'servicedesk', grantedBy: ['as2'] },
    ],
    warnings: [],
    ceilings: [],
    localAccount: false,
    excluded: [],
  };
  it('names the roles that grant a permission', () => {
    expect(grantReasons(effective, 'tickets.view').map((r) => r.roleKey)).toEqual(['a', 'b']);
    expect(grantReasons(effective, 'tickets.manage')[0]?.source).toBe('directory_group');
    expect(grantReasons(effective, 'unknown')).toEqual([]);
  });
  it('filters by text and risk', () => {
    expect(filterEffective(effective, { search: 'manage', risk: '' })).toHaveLength(1);
    expect(filterEffective(effective, { search: '', risk: 'normal' })).toHaveLength(1);
  });
});
