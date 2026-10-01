import type { AuthSession } from '../api/types';

export type CanFn = (permission: string) => boolean;

/** UI hiding only; the backend enforces every permission. */
export function createCan(session: Pick<AuthSession, 'permissions'> | null): CanFn {
  const granted = new Set(session?.permissions ?? []);
  return (permission) => granted.has(permission);
}

export function canAll(can: CanFn, required: readonly string[] | undefined): boolean {
  return !required || required.every((permission) => can(permission));
}
