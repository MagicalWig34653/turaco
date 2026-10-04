import { describe, expect, it } from 'vitest';
import { allowedActions, resourceLabel } from './helpers';
import type { Change } from './types';
describe('change helpers', () => {
  it('uses only operations supplied by the detail response', () => {
    const change = { status: 'assessment', requesterId: 'requester' } as Change;
    expect(allowedActions(change)).toEqual([]);
    expect(allowedActions({ ...change, allowedOperations: ['cancel'] })).toEqual(['cancel']);
    expect(allowedActions({ ...change, status: 'review', allowedOperations: [] })).toEqual([]);
  });
  it('never shows placeholder ids for hidden resources', () => {
    expect(
      resourceLabel(
        { type: 'asset', id: 'hidden-1', hidden: true },
        (type) => `Restricted ${type}`,
      ),
    ).toBe('Restricted asset');
  });
});
