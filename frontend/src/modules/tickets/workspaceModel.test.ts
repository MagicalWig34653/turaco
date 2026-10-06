import { describe, expect, it } from 'vitest';
import { appendWorkaround, clearSubmittedDraft, primaryTicketOperation } from './workspaceModel';

describe('ticket workspace', () => {
  it('promotes the next productive operation without inventing permission', () => {
    expect(primaryTicketOperation(['cancel', 'resolve', 'start'])).toBe('start');
    expect(primaryTicketOperation(['resolve', 'wait'])).toBe('resolve');
    expect(primaryTicketOperation(['reopen', 'close'])).toBe('close');
    expect(primaryTicketOperation(['cancel'])).toBeUndefined();
    expect(primaryTicketOperation([])).toBeUndefined();
  });
  it('keeps the other composer mode and newer text after a successful send', () => {
    const drafts = { reply: 'Public reply', internal: 'Private investigation' };
    expect(clearSubmittedDraft(drafts, 'reply', 'Public reply')).toEqual({
      reply: '',
      internal: 'Private investigation',
    });
    expect(clearSubmittedDraft(drafts, 'internal', 'Private investigation')).toEqual({
      reply: 'Public reply',
      internal: '',
    });
    expect(clearSubmittedDraft(drafts, 'reply', 'Earlier text')).toBe(drafts);
  });
  it('inserts a workaround without replacing the draft and respects the comment limit', () => {
    expect(appendWorkaround('My reply  ', ' Workaround ')).toBe('My reply\n\nWorkaround');
    expect(appendWorkaround('', ' Workaround ')).toBe('Workaround');
    expect(appendWorkaround('x'.repeat(4999), 'Workaround')).toHaveLength(5000);
  });
});
