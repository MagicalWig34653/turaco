import { describe, expect, it } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './api';

describe('organization error codes', () => {
  it('maps not found and invalid_* families', () => {
    expect(errorMessageKey({ code: 'organization.not_found', status: 404 })).toBe('error.notFound');
    expect(errorMessageKey({ code: 'organization.invalid_query', status: 400 })).toBe(
      'error.invalidRequest',
    );
  });
});
