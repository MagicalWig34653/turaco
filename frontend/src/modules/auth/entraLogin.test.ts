import { describe, expect, it } from 'vitest';
import type { AuthMethods } from '../../platform/api/types';

describe('auth methods', () => {
  it('treats a missing entra flag as not offered', () => {
    const legacy: AuthMethods = { password: true, kerberos: false, emergency: false };
    expect(legacy.entra).toBeUndefined();
    expect(Boolean(legacy.entra)).toBe(false);
  });
});
