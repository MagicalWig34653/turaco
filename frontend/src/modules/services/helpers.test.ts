import { describe, expect, it } from 'vitest';
import { impactTarget, impactUrl, nodeLabel } from './helpers';
describe('service view helpers', () => {
  it('uses the id when a visible node has no name', () =>
    expect(nodeLabel({ type: 'asset', id: 'a' })).toBe('a'));
  it('round trips an impact target safely', () =>
    expect(impactTarget(impactUrl('service', 'a&b').split('?')[1] ?? '')).toEqual({
      type: 'service',
      id: 'a&b',
    }));
  it('rejects unsupported targets', () => expect(impactTarget('?type=user&id=x')).toBeNull());
});
