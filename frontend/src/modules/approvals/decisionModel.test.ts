import { describe, expect, it } from 'vitest';
import { maxRefetches, shouldRefetchRequest } from './decisionModel';

describe('shouldRefetchRequest', () => {
  it('keeps polling while the decided request still awaits approval', () => {
    expect(shouldRefetchRequest('pending_approval', true, 0)).toBe(true);
    expect(shouldRefetchRequest('pending_approval', true, maxRefetches - 1)).toBe(true);
  });
  it('stops after the attempt limit and once the status moved on', () => {
    expect(shouldRefetchRequest('pending_approval', true, maxRefetches)).toBe(false);
    expect(shouldRefetchRequest('in_fulfillment', true, 0)).toBe(false);
  });
  it('does not poll before a decision', () => {
    expect(shouldRefetchRequest('pending_approval', false, 0)).toBe(false);
  });
});
