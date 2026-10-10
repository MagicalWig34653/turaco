import { describe, expect, it } from 'vitest';
import { reportedImpactKey } from './impactModel';

describe('reportedImpactKey', () => {
  it('maps the stored impact choice to its label', () => {
    expect(reportedImpactKey('blocked', false)).toBe('report.impact.blocked');
  });
  it('treats the patient flag without a choice as patient care', () => {
    expect(reportedImpactKey(null, true)).toBe('report.impact.patient_care');
    expect(reportedImpactKey('weird', true)).toBe('report.impact.patient_care');
  });
  it('shows nothing when no impact was given', () => {
    expect(reportedImpactKey(null, false)).toBeNull();
    expect(reportedImpactKey(undefined, undefined)).toBeNull();
  });
});
