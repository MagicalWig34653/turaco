import { useMemo } from 'react';
import { useSession } from '../../platform/session/SessionProvider';
import type { Actor } from './accessModel';

/** The signed-in person as the permission picker needs them (grant ceiling hints only). */
export function useActor(): Actor {
  const { can } = useSession();
  return useMemo(() => ({ isAdministrator: can('platform.admin'), has: can }), [can]);
}
