// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export type UserStatus = 'active' | 'inactive' | 'departed' | 'external' | 'unknown';

export type User = {
  id: string;
  displayName: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  status: UserStatus;
  updatedAt: string;
};

export type DirectoryGroup = {
  id: string;
  providerKey: string;
  externalId: string;
  displayName: string;
  description?: string | null;
  firstObservedAt: string;
  lastObservedAt: string;
  deletedObservedAt?: string | null;
};

export type Team = {
  id: string;
  name: string;
  active: boolean;
  updatedAt: string;
};

export type Location = {
  id: string;
  name: string;
  externalKey?: string | null;
  active: boolean;
  updatedAt: string;
};
