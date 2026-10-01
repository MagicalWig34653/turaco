import type { ApiError } from '../../platform/api/client';
import { retryAfterMinutes } from '../../platform/format/format';
import type { MessageKey, MessageParams } from '../../platform/i18n/i18n';

/** Maps a failed login call to a localized message; never reveals which credential was wrong. */
export function loginErrorMessage(error: Pick<ApiError, 'status' | 'retryAfterSeconds'>): {
  key: MessageKey;
  params?: MessageParams;
} {
  switch (error.status) {
    case 0:
      return { key: 'login.error.network' };
    case 400:
      return { key: 'login.error.invalidRequest' };
    case 401:
      return { key: 'login.error.invalidCredentials' };
    case 404:
      return { key: 'login.error.methodUnavailable' };
    case 429:
      return {
        key: 'login.error.tooManyAttempts',
        params: { minutes: retryAfterMinutes(error.retryAfterSeconds) },
      };
    case 503:
      return { key: 'login.error.providerUnavailable' };
    default:
      return { key: 'login.error.generic' };
  }
}
