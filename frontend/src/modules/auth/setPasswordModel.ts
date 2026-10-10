import type { ApiError } from '../../platform/api/client';
import type { MessageKey } from '../../platform/i18n/i18n';

/** Message for a failed redeem call; every token failure looks the same on purpose. */
export function redeemErrorKey(error: Pick<ApiError, 'status' | 'code'>): MessageKey {
  if (error.status === 0) return 'login.error.network';
  if (error.code === 'auth.password_policy') return 'setPassword.error.policy';
  if (error.code === 'auth.invalid_token') return 'setPassword.error.invalidToken';
  if (error.status === 404 || error.code === 'auth.method_unavailable')
    return 'setPassword.error.unavailable';
  if (error.status === 429) return 'setPassword.error.tooMany';
  if (error.status === 403) return 'setPassword.error.origin';
  if (error.status === 400) return 'setPassword.error.invalidToken';
  return 'setPassword.error.generic';
}

/** A link opened without a token in the fragment cannot be used. */
export type TokenState = 'present' | 'missing';
