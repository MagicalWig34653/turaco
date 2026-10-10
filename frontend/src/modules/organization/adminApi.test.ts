import { describe, expect, it } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './adminApi';
import '../access/api';

describe('F14 error codes', () => {
  it('maps People and Teams conflicts to plain messages', () => {
    expect(errorMessageKey({ code: 'organization.version_conflict', status: 409 })).toBe(
      'people.error.versionConflict',
    );
    expect(errorMessageKey({ code: 'organization.field_directory_owned', status: 409 })).toBe(
      'people.error.directoryOwned',
    );
    expect(errorMessageKey({ code: 'access.team_membership_self', status: 409 })).toBe(
      'people.error.teamMembershipSelf',
    );
    expect(errorMessageKey({ code: 'organization.emergency_account', status: 409 })).toBe(
      'people.error.emergencyAccount',
    );
  });
  it('maps escalation guards of roles and assignments', () => {
    expect(errorMessageKey({ code: 'access.dominance_required', status: 403 })).toBe(
      'access.error.dominanceRequired',
    );
    expect(errorMessageKey({ code: 'access.grant_exceeds_holder', status: 403 })).toBe(
      'access.error.grantExceedsHolder',
    );
    expect(errorMessageKey({ code: 'access.self_assignment', status: 409 })).toBe(
      'access.error.selfAssignment',
    );
    expect(errorMessageKey({ code: 'access.local_account_high_risk', status: 409 })).toBe(
      'access.error.localAccountHighRisk',
    );
  });
  it('maps the sign-in link errors an administrator can meet', () => {
    expect(errorMessageKey({ code: 'auth.base_url_not_configured', status: 409 })).toBe(
      'auth.error.baseUrlNotConfigured',
    );
    expect(errorMessageKey({ code: 'auth.mail_not_configured', status: 409 })).toBe(
      'auth.error.mailNotConfigured',
    );
    expect(errorMessageKey({ code: 'auth.mail_failed', status: 502 })).toBe(
      'auth.error.mailFailed',
    );
  });
});
