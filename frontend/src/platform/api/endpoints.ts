import { api, type ApiClient } from './client';
import type { AuthMethods, AuthSession, EmergencyLoginRequest, LoginRequest } from './types';

type Signal = AbortSignal | undefined;

/**
 * Typed wrappers for the auth/session endpoints used by the shell and the login screen.
 * Feature endpoints live in their module's `api.ts`.
 */
export function createEndpoints(client: ApiClient) {
  return {
    authMethods: (signal?: Signal) => client.get<AuthMethods>('/auth/methods', { signal }),
    login: (body: LoginRequest) =>
      client.post<void>('/auth/login', body, { skipUnauthorizedHandler: true }),
    emergencyLogin: (body: EmergencyLoginRequest) =>
      client.post<void>('/auth/emergency-login', body, { skipUnauthorizedHandler: true }),
    kerberosLogin: (signal?: Signal) =>
      client.get<void>('/auth/kerberos', { signal, skipUnauthorizedHandler: true }),
    session: (signal?: Signal) =>
      client.get<AuthSession>('/auth/session', { signal, skipUnauthorizedHandler: true }),
    logout: () =>
      client.post<{ redirectUrl?: string } | undefined>('/auth/logout', undefined, {
        skipUnauthorizedHandler: true,
      }),
  };
}

export const endpoints = createEndpoints(api);
