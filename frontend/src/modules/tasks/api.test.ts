import { describe, expect, it } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './api';

describe('tasks error codes', () => {
  it('maps task codes to localized messages', () => {
    expect(errorMessageKey({ code: 'tasks.version_conflict', status: 409 })).toBe(
      'error.versionConflict',
    );
    expect(errorMessageKey({ code: 'tasks.invalid_transition', status: 409 })).toBe(
      'error.invalidTransition',
    );
    expect(errorMessageKey({ code: 'tasks.assignee_invalid', status: 400 })).toBe(
      'error.assigneeInvalid',
    );
    expect(errorMessageKey({ code: 'tasks.invalid_request', status: 400 })).toBe(
      'error.invalidRequest',
    );
    expect(errorMessageKey({ code: 'tasks.invalid_limit', status: 400 })).toBe(
      'error.invalidRequest',
    );
  });
});
