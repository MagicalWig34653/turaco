// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type AuthMethods = { password: boolean; kerberos: boolean; emergency: boolean };

export type AuthSession = {
  userId: string;
  authMethod: string;
  expiresAt: string;
  permissions: string[];
};

export type LoginRequest = { identifier: string; password: string };
export type EmergencyLoginRequest = { login: string; password: string };

export type Page<T> = { items: T[]; nextCursor?: string };
