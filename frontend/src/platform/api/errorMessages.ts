import type { MessageKey } from '../i18n/i18n';
import { INVALID_RESPONSE_CODE, NETWORK_ERROR_CODE, type ApiError } from './client';

const byCode: Record<string, MessageKey> = {
  [NETWORK_ERROR_CODE]: 'error.network',
  [INVALID_RESPONSE_CODE]: 'error.invalidResponse',
  'platform.unauthenticated': 'error.unauthenticated',
  'platform.forbidden': 'error.forbidden',
  'platform.csrf_rejected': 'error.csrf',
  'platform.internal_error': 'error.internal',
  'organization.not_found': 'error.notFound',
  'organization.directory_sync_not_configured': 'error.directorySyncNotConfigured',
  'authorization.not_found': 'error.notFound',
  'authorization.subject_not_found': 'error.subjectNotFound',
  'authorization.invalid_request': 'error.invalidRequest',
  'authorization.unknown_permission': 'error.unknownPermission',
  'authorization.duplicate_key': 'error.duplicateKey',
  'authorization.built_in_role': 'error.builtInRole',
  'authorization.role_in_use': 'error.roleInUse',
  'authorization.duplicate_assignment': 'error.duplicateAssignment',
  'authorization.last_administrator': 'error.lastAdministrator',
  'audit.invalid_filter': 'error.invalidFilter',
};

/** Localized message key for an API error; the server's English message is never shown directly. */
export function errorMessageKey(error: Pick<ApiError, 'code' | 'status'>): MessageKey {
  const known = byCode[error.code];
  if (known) return known;
  if (error.code.endsWith('invalid_cursor') || error.code.endsWith('invalid_limit')) {
    return 'error.invalidRequest';
  }
  if (error.code.startsWith('organization.invalid_')) return 'error.invalidRequest';
  if (error.status === 401) return 'error.unauthenticated';
  if (error.status === 403) return 'error.forbidden';
  if (error.status === 404) return 'error.notFound';
  if (error.status >= 500) return 'error.internal';
  return 'error.generic';
}
