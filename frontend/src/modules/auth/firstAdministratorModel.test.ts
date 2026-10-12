import { describe, expect, it } from 'vitest';
import { firstAdministratorErrorKey, firstAdministratorRequest } from './firstAdministratorModel';

describe('firstAdministratorRequest', () => {
  const tenant = '11111111-1111-1111-1111-111111111111';
  const object = '22222222-2222-2222-2222-222222222222';

  it('sends a directory user with no optional identity', () => {
    expect(firstAdministratorRequest(' token ', ' user@example.com ', '', '')).toEqual({
      token: 'token',
      user: 'user@example.com',
    });
  });

  it('requires both valid Entra GUIDs', () => {
    expect(firstAdministratorRequest('token', 'user', tenant, object)).toEqual({
      token: 'token',
      user: 'user',
      tenant,
      object,
    });
    expect(firstAdministratorRequest('token', 'user', tenant, '')).toBeNull();
    expect(firstAdministratorRequest('token', 'user', 'invalid', object)).toBeNull();
  });

  it('requires a token and user', () => {
    expect(firstAdministratorRequest('', 'user', '', '')).toBeNull();
    expect(firstAdministratorRequest('token', ' ', '', '')).toBeNull();
  });
});

describe('firstAdministratorErrorKey', () => {
  it.each([
    ['setup.invalid_request', 'firstAdmin.error.invalidRequest'],
    ['setup.unavailable', 'firstAdmin.unavailable'],
    ['setup.user_not_found', 'firstAdmin.error.userNotFound'],
    ['setup.local_account', 'firstAdmin.error.localAccount'],
    ['setup.entra_link_failed', 'firstAdmin.error.entraLink'],
    ['setup.rate_limited', 'firstAdmin.error.rateLimited'],
  ] as const)('maps %s', (code, key) => {
    expect(firstAdministratorErrorKey({ status: 400, code })).toBe(key);
  });

  it('has generic and network fallbacks', () => {
    expect(firstAdministratorErrorKey({ status: 500, code: 'internal_error' })).toBe(
      'firstAdmin.error.generic',
    );
    expect(firstAdministratorErrorKey({ status: 0, code: 'platform.network_error' })).toBe(
      'login.error.network',
    );
  });
});
