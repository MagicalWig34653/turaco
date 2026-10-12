import type { ApiError } from '../../platform/api/client';
import type { MessageKey } from '../../platform/i18n/i18n';

const guid = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

export type FirstAdministratorRequest = {
  token: string;
  user: string;
  tenant?: string;
  object?: string;
};

export function firstAdministratorRequest(
  token: string,
  user: string,
  tenant: string,
  object: string,
): FirstAdministratorRequest | null {
  const trimmedToken = token.trim();
  const trimmedUser = user.trim();
  const trimmedTenant = tenant.trim();
  const trimmedObject = object.trim();
  if (!trimmedToken || !trimmedUser || trimmedUser.length > 320) return null;
  if (trimmedTenant || trimmedObject) {
    if (!guid.test(trimmedTenant) || !guid.test(trimmedObject)) return null;
    return { token: trimmedToken, user: trimmedUser, tenant: trimmedTenant, object: trimmedObject };
  }
  return { token: trimmedToken, user: trimmedUser };
}

export function firstAdministratorErrorKey(error: Pick<ApiError, 'status' | 'code'>): MessageKey {
  switch (error.code) {
    case 'setup.invalid_request':
      return 'firstAdmin.error.invalidRequest';
    case 'setup.unavailable':
      return 'firstAdmin.unavailable';
    case 'setup.user_not_found':
      return 'firstAdmin.error.userNotFound';
    case 'setup.local_account':
      return 'firstAdmin.error.localAccount';
    case 'setup.entra_link_failed':
      return 'firstAdmin.error.entraLink';
    case 'setup.rate_limited':
      return 'firstAdmin.error.rateLimited';
    default:
      return error.status === 0 ? 'login.error.network' : 'firstAdmin.error.generic';
  }
}
