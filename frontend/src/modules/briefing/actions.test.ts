import { describe, expect, it } from 'vitest';
import { createCan } from '../../platform/session/permissions';
import { availableActions, isExpired, severityTone } from './actions';

const can = (...permissions: string[]) => createCan({ permissions });

describe('availableActions', () => {
  it('lets managers edit, publish and delete drafts, and withdraw published items', () => {
    expect(availableActions({ status: 'draft' }, can('briefing.manage'))).toEqual([
      'edit',
      'publish',
      'delete',
    ]);
    expect(availableActions({ status: 'published' }, can('briefing.manage'))).toEqual(['withdraw']);
    expect(availableActions({ status: 'withdrawn' }, can('briefing.manage'))).toEqual([]);
  });

  it('offers viewers and others nothing', () => {
    for (const status of ['draft', 'published', 'withdrawn'] as const) {
      expect(availableActions({ status }, can('briefing.view'))).toEqual([]);
      expect(availableActions({ status }, can())).toEqual([]);
    }
  });
});

describe('severityTone / isExpired', () => {
  it('maps severities to badge tones', () => {
    expect(severityTone('info')).toBe('info');
    expect(severityTone('warning')).toBe('warning');
    expect(severityTone('critical')).toBe('danger');
  });

  it('detects expiry', () => {
    const now = new Date('2026-10-02T12:00:00Z');
    expect(isExpired({ validUntil: null }, now)).toBe(false);
    expect(isExpired({ validUntil: '2026-10-02T11:00:00Z' }, now)).toBe(true);
    expect(isExpired({ validUntil: '2026-10-02T12:00:00Z' }, now)).toBe(true);
    expect(isExpired({ validUntil: '2026-10-03T00:00:00Z' }, now)).toBe(false);
    expect(isExpired({ validUntil: 'garbage' }, now)).toBe(false);
  });
});
