import { describe, expect, it } from 'vitest';
import { createCan } from './permissions';

describe('createCan', () => {
  it('counts a permission held only at Locations as held anywhere', () => {
    const can = createCan({
      permissions: ['organization.view'],
      scopedPermissions: { 'assets.view': ['loc-a'], 'tickets.view': [] },
    });
    expect(can('organization.view')).toBe(true);
    expect(can('assets.view')).toBe(true);
    expect(can('tickets.view')).toBe(false);
    expect(can('assets.manage')).toBe(false);
  });
  it('is empty without a session', () => {
    expect(createCan(null)('assets.view')).toBe(false);
  });
});
