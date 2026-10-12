// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type AuthMethods = {
  password: boolean;
  kerberos: boolean;
  emergency: boolean;
  /** Sign-in with Microsoft Entra ID is configured; absent on servers that do not report it. */
  entra?: boolean;
};

export type AuthSession = {
  userId: string;
  authMethod: string;
  expiresAt: string;
  /** Permissions held globally. */
  permissions: string[];
  /**
   * Permissions held only at Locations (ADR-0040): permission -> Location ids. Not used for gating yet
   * (hasPermissionAnywhere arrives with F16 slice L-F).
   */
  scopedPermissions?: Record<string, string[]>;
  /** The user's own display name; presentation only, absent when unknown. */
  displayName?: string;
  /** The user's given name when the directory provides one. */
  givenName?: string;
  /** The user's preferred UI language when the profile has one ('de' or 'en'). */
  locale?: string;
  /** A restricted external account (ADR-0034): only its Tasks and the Tickets shared with it. */
  external?: boolean;
};

export type LoginRequest = { identifier: string; password: string };
export type EmergencyLoginRequest = { login: string; password: string };

export type Page<T> = { items: T[]; nextCursor?: string };
