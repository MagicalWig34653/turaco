import type { AuthSession } from '../api/types';

type Names = Pick<AuthSession, 'userId' | 'displayName' | 'givenName'>;

const opaqueId = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** A name is shown only when it is a real name, never an identifier. */
function usable(value: string | undefined, userId: string): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed || trimmed === userId || opaqueId.test(trimmed)) return undefined;
  return trimmed;
}

/** The user's full display name for identity chips; undefined when unknown. */
export function sessionDisplayName(session: Names | null | undefined): string | undefined {
  if (!session) return undefined;
  return usable(session.displayName, session.userId);
}

/**
 * The name used in a greeting: the given name when the directory has one,
 * otherwise the full display name. A display name is never cut into words,
 * because "Development Admin" is not a person called "Development".
 */
export function greetingName(session: Names | null | undefined): string | undefined {
  if (!session) return undefined;
  return usable(session.givenName, session.userId) ?? sessionDisplayName(session);
}

export type DayPart = 'morning' | 'afternoon' | 'evening';

export function dayPart(date: Date): DayPart {
  const hour = date.getHours();
  if (hour >= 5 && hour < 12) return 'morning';
  if (hour >= 12 && hour < 18) return 'afternoon';
  return 'evening';
}
