// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type AuthMethods = { password: boolean; kerberos: boolean; emergency: boolean };

export type AuthSession = {
  userId: string;
  authMethod: string;
  expiresAt: string;
  permissions: string[];
  /** The user's own display name; presentation only, absent when unknown. */
  displayName?: string;
  /** The user's given name when the directory provides one. */
  givenName?: string;
  /** The user's preferred UI language when the profile has one ('de' or 'en'). */
  locale?: string;
};

export type LoginRequest = { identifier: string; password: string };
export type EmergencyLoginRequest = { login: string; password: string };

export type Page<T> = { items: T[]; nextCursor?: string };
