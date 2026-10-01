import { describe, expect, it } from 'vitest';
import { loginErrorMessage } from './loginErrors';

describe('loginErrorMessage', () => {
  it('maps statuses to localized message keys', () => {
    expect(loginErrorMessage({ status: 401, retryAfterSeconds: undefined }).key).toBe(
      'login.error.invalidCredentials',
    );
    expect(loginErrorMessage({ status: 503, retryAfterSeconds: undefined }).key).toBe(
      'login.error.providerUnavailable',
    );
    expect(loginErrorMessage({ status: 400, retryAfterSeconds: undefined }).key).toBe(
      'login.error.invalidRequest',
    );
    expect(loginErrorMessage({ status: 0, retryAfterSeconds: undefined }).key).toBe(
      'login.error.network',
    );
    expect(loginErrorMessage({ status: 500, retryAfterSeconds: undefined }).key).toBe(
      'login.error.generic',
    );
  });

  it('maps auth.temporarily_unavailable and short 429 Retry-After', () => {
    expect(
      loginErrorMessage({
        status: 503,
        code: 'auth.temporarily_unavailable',
        retryAfterSeconds: undefined,
      }).key,
    ).toBe('login.error.temporarilyUnavailable');
    expect(loginErrorMessage({ status: 429, retryAfterSeconds: 5 })).toEqual({
      key: 'login.error.busy',
    });
  });

  it('shows Retry-After as minutes for 429', () => {
    expect(loginErrorMessage({ status: 429, retryAfterSeconds: 610 })).toEqual({
      key: 'login.error.tooManyAttempts',
      params: { minutes: 11 },
    });
  });
});
