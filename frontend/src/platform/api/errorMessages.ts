import type { MessageKey } from '../i18n/i18n';
import { INVALID_RESPONSE_CODE, NETWORK_ERROR_CODE, type ApiError } from './client';

type ErrorLike = Pick<ApiError, 'code' | 'status'> & Partial<Pick<ApiError, 'message'>>;

/** Platform and auth/session codes only; feature modules register their own codes. */
const byCode: Record<string, MessageKey> = {
  [NETWORK_ERROR_CODE]: 'error.network',
  [INVALID_RESPONSE_CODE]: 'error.invalidResponse',
  'platform.unauthenticated': 'error.unauthenticated',
  'platform.module_disabled': 'modules.disabledInfo',
  'platform.forbidden': 'error.forbidden',
  'platform.csrf_rejected': 'error.csrf',
  'platform.internal_error': 'error.internal',
  'auth.temporarily_unavailable': 'login.error.temporarilyUnavailable',
};

const resolvers: Array<(error: ErrorLike) => MessageKey | undefined> = [];

/** Lets a feature module map its own error codes to message keys (call at module load). */
export function registerErrorMessages(codes: Record<string, MessageKey>): void {
  Object.assign(byCode, codes);
}

/** Lets a feature module map code families (e.g. a prefix) to a message key. */
export function registerErrorResolver(
  resolver: (error: ErrorLike) => MessageKey | undefined,
): void {
  resolvers.push(resolver);
}

/** Localized message key for an API error; the server's English message is never shown directly. */
export function errorMessageKey(error: ErrorLike): MessageKey {
  const known = byCode[error.code];
  if (known) return known;
  for (const resolve of resolvers) {
    const key = resolve(error);
    if (key) return key;
  }
  if (error.code.endsWith('invalid_cursor') || error.code.endsWith('invalid_limit')) {
    return 'error.invalidRequest';
  }
  if (error.status === 401) return 'error.unauthenticated';
  if (error.status === 403) return 'error.forbidden';
  if (error.status === 404) return 'error.notFound';
  if (error.status >= 500) return 'error.internal';
  return 'error.generic';
}
