/** A timer older than this needs an explicit duration to stop (server rule, design section 7). */
export const staleTimerMs = 12 * 60 * 60 * 1000;

/** Elapsed whole minutes, rounded up, at least one (what the server logs for a fresh timer). */
export function elapsedMinutes(startedAt: string, now: number): number {
  const ms = now - Date.parse(startedAt);
  if (!Number.isFinite(ms) || ms <= 0) return 1;
  return Math.max(1, Math.ceil(ms / 60000));
}

export function isStaleTimer(startedAt: string, now: number): boolean {
  const ms = now - Date.parse(startedAt);
  return Number.isFinite(ms) && ms > staleTimerMs;
}
