import type { ExternalIdentity, PersonRow } from './adminTypes';

/** Prefix of the provider key of an Entra tenant (`entra:<tenant id>`). */
export const ENTRA_PREFIX = 'entra:';

const GUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** A GUID as copied from the Entra portal (case and surrounding spaces do not matter). */
export function isGuid(value: string): boolean {
  return GUID.test(value.trim().toLowerCase());
}

export function normalizeGuid(value: string): string {
  return value.trim().toLowerCase();
}

export const isEntraIdentity = (identity: Pick<ExternalIdentity, 'providerKey'>): boolean =>
  identity.providerKey.startsWith(ENTRA_PREFIX);

/** Tenant id of an Entra identity's provider key. */
export const entraTenantOf = (identity: Pick<ExternalIdentity, 'providerKey'>): string =>
  identity.providerKey.slice(ENTRA_PREFIX.length);

/**
 * Presentation only (the server authorizes): platform administrators may link and unlink Entra identities of
 * employee accounts that are not the emergency account and not their own account.
 */
export function entraActions(
  person: Pick<PersonRow, 'id' | 'source' | 'accountKind' | 'status'>,
  can: (permission: string) => boolean,
  ownId: string | undefined,
): { link: boolean; unlink: boolean } {
  const allowed =
    can('platform.admin') &&
    person.source !== 'emergency' &&
    person.accountKind === 'employee' &&
    person.id !== ownId;
  return { link: allowed && person.status === 'active', unlink: allowed };
}

export type EntraFormErrors = { tenant?: 'required' | 'invalid'; object?: 'required' | 'invalid' };

/** Validates the link form; the server validates again. */
export function entraFormErrors(tenantId: string, objectId: string): EntraFormErrors {
  const errors: EntraFormErrors = {};
  if (tenantId.trim() === '') errors.tenant = 'required';
  else if (!isGuid(tenantId)) errors.tenant = 'invalid';
  if (objectId.trim() === '') errors.object = 'required';
  else if (!isGuid(objectId)) errors.object = 'invalid';
  return errors;
}
