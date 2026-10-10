/**
 * A decision is applied asynchronously (an outbox consumer starts fulfilment), so the request read
 * right after the decision can still say "awaiting approval". Re-read it a few times.
 */
export const refetchDelayMs = 1000;
export const maxRefetches = 5;

export function shouldRefetchRequest(
  status: string | undefined,
  decided: boolean,
  attempt: number,
): boolean {
  return decided && status === 'pending_approval' && attempt < maxRefetches;
}
