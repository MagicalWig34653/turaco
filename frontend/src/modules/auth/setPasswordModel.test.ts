import { describe, expect, it } from 'vitest';
import { redeemErrorKey } from './setPasswordModel';

describe('redeemErrorKey', () => {
  it('keeps every token failure identical', () => {
    expect(redeemErrorKey({ status: 400, code: 'auth.invalid_token' })).toBe(
      'setPassword.error.invalidToken',
    );
    expect(redeemErrorKey({ status: 400, code: 'auth.invalid_request' })).toBe(
      'setPassword.error.invalidToken',
    );
  });
  it('explains the password policy separately so the person can retry', () => {
    expect(redeemErrorKey({ status: 400, code: 'auth.password_policy' })).toBe(
      'setPassword.error.policy',
    );
  });
  it('maps switched-off local accounts, throttling, origin and transport errors', () => {
    expect(redeemErrorKey({ status: 404, code: 'auth.method_unavailable' })).toBe(
      'setPassword.error.unavailable',
    );
    expect(redeemErrorKey({ status: 429, code: 'auth.too_many_attempts' })).toBe(
      'setPassword.error.tooMany',
    );
    expect(redeemErrorKey({ status: 403, code: 'platform.csrf_rejected' })).toBe(
      'setPassword.error.origin',
    );
    expect(redeemErrorKey({ status: 0, code: 'platform.network_error' })).toBe(
      'login.error.network',
    );
    expect(redeemErrorKey({ status: 500, code: 'x' })).toBe('setPassword.error.generic');
  });
});
