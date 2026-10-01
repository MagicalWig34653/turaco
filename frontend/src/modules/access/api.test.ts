import { describe, expect, it } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './api';

describe('access error codes', () => {
  it('registers authorization codes', () => {
    expect(errorMessageKey({ code: 'authorization.last_administrator', status: 409 })).toBe(
      'error.lastAdministrator',
    );
    expect(errorMessageKey({ code: 'authorization.duplicate_key', status: 409 })).toBe(
      'error.duplicateKey',
    );
  });
});
