import type { TicketAbilities, TicketOperation } from './types';

/** Choose a prominent operation only from the backend's allowed operations. */
export function primaryTicketOperation(operations: readonly TicketOperation[]) {
  const order: TicketOperation[] = ['start', 'resume', 'resolve', 'close', 'reopen', 'wait'];
  return order.find((operation) => operations.includes(operation));
}

/** Adds the workaround below the draft; `source` is a ready-made line such as "Workaround from PRB-1:". */
export function appendWorkaround(draft: string, workaround: string, source = ''): string {
  const text = workaround.trim();
  const block = text && source.trim() ? `${source.trim()}\n${text}` : text;
  return [draft.trimEnd(), block].filter(Boolean).join('\n\n').slice(0, 5000);
}

/** A successful send must not erase another mode or edits made while it was pending. */
export function clearSubmittedDraft(
  drafts: { reply: string; internal: string },
  mode: 'reply' | 'internal',
  submitted: string,
) {
  return drafts[mode] === submitted ? { ...drafts, [mode]: '' } : drafts;
}

/** The permission and ownership facts used only when a server does not return `abilities`. */
export type AbilityFallback = {
  manage: boolean;
  isOwner: boolean;
  open: boolean;
  canMove: boolean;
};

export type ResolvedAbilities = Omit<TicketAbilities, 'setLocation'> & {
  /** The affected person's location may be corrected; only a server that says so offers it. */
  setLocation: boolean;
  /** The composer is shown when at least one comment kind is allowed. */
  composer: boolean;
  /** Both kinds are allowed, so the reply/internal switch is offered. */
  canChooseKind: boolean;
};

/**
 * The server's per-Ticket abilities decide the composer and the action buttons. A global permission
 * is only used when an older server sends no abilities; the server still authorizes every call.
 */
export function resolveAbilities(
  abilities: TicketAbilities | undefined,
  fallback: AbilityFallback,
): ResolvedAbilities {
  const base: TicketAbilities = abilities ?? {
    comment: fallback.open && (fallback.manage || fallback.isOwner),
    internalComment: fallback.open && fallback.manage,
    assign: fallback.manage,
    setPriority: fallback.manage,
    transition: true,
    move: fallback.canMove,
    markDuplicate: fallback.manage,
    setLocation: false,
  };
  return {
    ...base,
    setLocation: base.setLocation === true,
    composer: base.comment || base.internalComment,
    canChooseKind: base.comment && base.internalComment,
  };
}

/** Which comment kind is used given the allowed kinds and the author's choice. */
export function effectiveCommentKind(
  abilities: Pick<TicketAbilities, 'comment' | 'internalComment'>,
  internalChoice: boolean,
): 'reply' | 'internal' {
  if (!abilities.comment) return 'internal';
  if (!abilities.internalComment) return 'reply';
  return internalChoice ? 'internal' : 'reply';
}
