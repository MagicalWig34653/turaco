import { describe, expect, it } from 'vitest';
import {
  advisoryActions,
  devicePath,
  findingActions,
  safeSourceUrl,
  validReviewDate,
  overviewLinks,
  isOpenFinding,
  isApplicableAdvisory,
  riskReviewDueWithin30Days,
} from './helpers';
import type { Finding } from './types';
describe('security view helpers', () => {
  it('keeps lifecycle actions inside the available states and permissions', () => {
    expect(advisoryActions('archived')).toEqual([]);
    expect(advisoryActions('applicable')).toContain('resolve');
    expect(findingActions('open', false, false)).toEqual([]);
    expect(findingActions('open', false, true)).toEqual(['accept-risk']);
    expect(findingActions('risk_accepted', false, true)).toEqual(['reopen']);
    expect(findingActions('remediating', true, false)).toEqual(['false-positive']);
  });
  it('does not link redacted devices or unsafe source URLs', () => {
    const finding = { deviceId: 'abc', deviceHidden: true } as Finding;
    expect(devicePath(finding, true)).toBeNull();
    expect(devicePath({ ...finding, deviceHidden: false }, true)).toBe('/devices/abc');
    expect(devicePath({ ...finding, deviceHidden: false }, false)).toBeNull();
    expect(safeSourceUrl('javascript:alert(1)')).toBeNull();
    expect(safeSourceUrl('https://example.test/a')).toBe('https://example.test/a');
  });
  it('requires a future risk review no later than twelve months', () => {
    const now = new Date(2026, 9, 4);
    expect(validReviewDate('2026-10-04', now)).toBe(false);
    expect(validReviewDate('2027-10-04', now)).toBe(true);
    expect(validReviewDate('2027-10-05', now)).toBe(false);
  });
  it('links overview counts to filtered lists', () => {
    expect(overviewLinks.severity('high')).toBe(
      '/security/advisories?applicable=true&severity=high',
    );
    expect(overviewLinks.confidence('probable')).toBe(
      '/security/findings?open=true&confidence=probable',
    );
    expect(isApplicableAdvisory('remediating')).toBe(true);
    expect(isApplicableAdvisory('resolved')).toBe(false);
    expect(isOpenFinding('remediated')).toBe(false);
    const finding = { status: 'risk_accepted', riskReviewBy: '2026-10-30' } as Finding;
    expect(riskReviewDueWithin30Days(finding, new Date(2026, 9, 5))).toBe(true);
    expect(
      riskReviewDueWithin30Days({ ...finding, riskReviewBy: '2026-10-04' }, new Date(2026, 9, 5)),
    ).toBe(false);
    expect(
      riskReviewDueWithin30Days({ ...finding, riskReviewBy: '2026-11-05' }, new Date(2026, 9, 5)),
    ).toBe(false);
  });
});
