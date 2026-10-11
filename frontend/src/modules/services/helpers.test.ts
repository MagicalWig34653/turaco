import { describe, expect, it } from 'vitest';
import { impactTarget, impactUrl, nodeLabel, teamName } from './helpers';
describe('service view helpers', () => {
  it('uses the id when a visible node has no name', () =>
    expect(nodeLabel({ type: 'asset', id: 'a' })).toBe('a'));
  it('round trips an impact target safely', () =>
    expect(impactTarget(impactUrl('service', 'a&b').split('?')[1] ?? '')).toEqual({
      type: 'service',
      id: 'a&b',
    }));
  it('rejects unsupported targets', () => expect(impactTarget('?type=user&id=x')).toBeNull());
  it('resolves team names and does not return an unknown team ID', () => {
    const teams = [{ id: 'team-1', name: 'Clinical IT' }] as Parameters<typeof teamName>[0];
    expect(teamName(teams, 'team-1')).toBe('Clinical IT');
    expect(teamName(teams, 'missing')).toBeUndefined();
  });
});
