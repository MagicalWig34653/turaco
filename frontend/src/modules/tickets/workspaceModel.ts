import type { TicketOperation } from './types';

/** Choose a prominent operation only from the backend's allowed operations. */
export function primaryTicketOperation(operations: readonly TicketOperation[]) {
  const order: TicketOperation[] = ['start', 'resume', 'resolve', 'close', 'reopen', 'wait'];
  return order.find((operation) => operations.includes(operation));
}

export function appendWorkaround(draft: string, workaround: string): string {
  return [draft.trimEnd(), workaround.trim()].filter(Boolean).join('\n\n').slice(0, 5000);
}

/** A successful send must not erase another mode or edits made while it was pending. */
export function clearSubmittedDraft(
  drafts: { reply: string; internal: string },
  mode: 'reply' | 'internal',
  submitted: string,
) {
  return drafts[mode] === submitted ? { ...drafts, [mode]: '' } : drafts;
}
