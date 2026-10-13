import type { AuthSession } from '../api/types';

export type CanFn = (permission: string) => boolean;

/**
 * UI hiding only; the backend enforces every permission. A permission held only at Locations (ADR-0040, F16) counts as
 * held "anywhere": screens and actions show for the holder, and the server then limits what they contain to the
 * Locations in scope. The session lists scoped permissions only for permissions whose module enforces Location scope.
 */
export function createCan(
  session: Pick<AuthSession, 'permissions' | 'scopedPermissions'> | null,
): CanFn {
  const granted = new Set(session?.permissions ?? []);
  for (const [permission, locations] of Object.entries(session?.scopedPermissions ?? {})) {
    if (locations.length > 0) granted.add(permission);
  }
  return (permission) => granted.has(permission);
}

export function canAll(can: CanFn, required: readonly string[] | undefined): boolean {
  return !required || required.every((permission) => can(permission));
}
