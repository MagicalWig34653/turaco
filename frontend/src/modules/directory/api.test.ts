import { describe, expect, it } from 'vitest';
import { errorMessageKey } from '../../platform/api/errorMessages';
import './api';

describe('directory error codes', () => {
  it('registers directory sync codes', () => {
    expect(
      errorMessageKey({ code: 'organization.directory_sync_not_configured', status: 409 }),
    ).toBe('error.directorySyncNotConfigured');
  });
});
