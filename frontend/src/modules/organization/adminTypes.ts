// Types mirror api/openapi/openapi.yaml (F14 People, Teams, Locations, Departments).

export type AccountSource = 'directory' | 'local' | 'emergency';
export type AccountKind = 'employee' | 'external';
export type StatusSource = 'platform' | 'directory';

/** A row of `POST /users/query` and the response of every user write. */
export type PersonRow = {
  id: string;
  displayName: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  status: string;
  statusSource: StatusSource;
  accountKind: AccountKind;
  source: AccountSource;
  version: number;
  updatedAt: string;
  departmentId?: string | null;
  primaryLocationId?: string | null;
  managerUserId?: string | null;
  employeeNumber?: string | null;
  accessExpiresAt?: string | null;
};

/** Owner of one attribute: who may change it, where the value came from and how fresh it is. */
export type FieldOwner = {
  key: string;
  owner: 'directory' | 'platform';
  source: string;
  observedAt?: string | null;
};

export type ExternalIdentity = {
  providerKey: string;
  username: string | null;
  enabled: boolean;
  lastSeenAt: string | null;
  deletedObservedAt: string | null;
};

export type PersonDetail = PersonRow & {
  fields: FieldOwner[];
  externalIdentities: ExternalIdentity[];
};

export type PersonCreate = {
  displayName: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  employeeNumber?: string | null;
  departmentId?: string | null;
  primaryLocationId?: string | null;
  accountKind?: 'employee';
};

export type ProfileUpdate = {
  expectedVersion: number;
  displayName?: string;
  givenName?: string | null;
  familyName?: string | null;
  primaryEmail?: string | null;
  employeeNumber?: string | null;
};

/** Result of an invitation or reset: `link` appears once and only for an invitation. */
export type CredentialLink = { expiresAt: string; mailed: boolean; link?: string };

export type TeamRole = 'lead' | 'member';

export type TeamMemberRow = {
  userId: string;
  displayName: string;
  role?: string | null;
  source: string;
  validFrom: string;
};

export type TeamRow = {
  id: string;
  name: string;
  description: string;
  active: boolean;
  version: number;
  updatedAt: string;
  leads?: TeamMemberRow[];
};

export type LocationKind = 'site' | 'area';

export type LocationNode = {
  id: string;
  name: string;
  kind: LocationKind;
  parentId?: string | null;
  code?: string | null;
  description: string;
  externalKey?: string | null;
  active: boolean;
  version: number;
  updatedAt: string;
};

export type DepartmentNode = {
  id: string;
  name: string;
  code?: string | null;
  parentId?: string | null;
  externalKey?: string | null;
  active: boolean;
  version: number;
  updatedAt: string;
};

export type OrgNode = {
  id: string;
  name: string;
  code?: string | null;
  parentId?: string | null;
  active: boolean;
  version: number;
  kind?: LocationKind;
  description?: string;
};

export type AuthMethodsInfo = {
  local: boolean;
  password: boolean;
  kerberos: boolean;
  emergency: boolean;
};
