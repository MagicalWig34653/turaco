import { describe, expect, it } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './api';

describe('audit error codes', () => {
  it('registers audit filter code', () => {
    expect(errorMessageKey({ code: 'audit.invalid_filter', status: 400 })).toBe(
      'error.invalidFilter',
    );
  });
});
