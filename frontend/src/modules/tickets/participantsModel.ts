/** The longest a Ticket can be shared in one go (days); the server enforces the same limit. */
export const MAX_PARTICIPANT_DAYS = 365;

/**
 * The instant sent to the server for an optional end-of-sharing day: the end of that local day, which must lie
 * in the future and within MAX_PARTICIPANT_DAYS of now. An empty day means no end and returns undefined, as does
 * an unusable day; callers tell the two apart by checking whether the day is empty.
 */
export function participantExpiry(day: string, now: Date): string | undefined {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(day);
  if (!match) return undefined;
  const end = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 23, 59, 0, 0);
  if (Number.isNaN(end.getTime())) return undefined;
  const limit = now.getTime() + MAX_PARTICIPANT_DAYS * 86_400_000 - 60_000;
  if (end.getTime() <= now.getTime()) return undefined;
  return new Date(Math.min(end.getTime(), limit)).toISOString();
}
