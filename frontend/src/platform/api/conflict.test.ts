import { describe, expect, it } from 'vitest';
import { ApiError } from './client';
import { needsImpactConfirmation, needsRuleAcknowledgement, parseConflict } from './conflict';

const conflict = (code: string, details: unknown) =>
  new ApiError({ status: 409, code, message: '', details });

describe('parseConflict', () => {
  it('reads counts, rules and fields', () => {
    expect(
      parseConflict({
        details: {
          counts: { users: 2, assets: 0 },
          rules: ['a'],
          fields: [{ field: 'displayName', code: 'x' }, 'primaryEmail'],
        },
      }),
    ).toEqual({ counts: { users: 2 }, rules: ['a'], fields: ['displayName', 'primaryEmail'] });
  });
  it('tolerates missing or malformed details', () => {
    const empty = { counts: {}, rules: [], fields: [] };
    expect(parseConflict({ details: undefined })).toEqual(empty);
    expect(parseConflict({ details: 'oops' })).toEqual(empty);
    expect(parseConflict({ details: { counts: [1], rules: 3 } })).toEqual(empty);
  });
});

describe('conflict kinds', () => {
  it('detects an impact confirmation by code or by counts', () => {
    expect(needsImpactConfirmation(conflict('organization.impact_confirmation_required', {}))).toBe(
      true,
    );
    expect(
      needsImpactConfirmation(conflict('organization.conflict', { counts: { users: 1 } })),
    ).toBe(true);
    expect(needsImpactConfirmation(conflict('organization.version_conflict', undefined))).toBe(
      false,
    );
  });
  it('detects separation-of-duties acknowledgement', () => {
    expect(
      needsRuleAcknowledgement(conflict('access.sod_acknowledgement_required', { rules: ['r'] })),
    ).toBe(true);
    expect(needsRuleAcknowledgement(conflict('authorization.duplicate_key', undefined))).toBe(
      false,
    );
  });
});
