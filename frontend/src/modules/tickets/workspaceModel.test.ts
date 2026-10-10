import { describe, expect, it } from 'vitest';
import {
  appendWorkaround,
  clearSubmittedDraft,
  effectiveCommentKind,
  primaryTicketOperation,
  resolveAbilities,
} from './workspaceModel';

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
    expect(appendWorkaround('Hi', 'Reboot', 'From PRB-1:')).toBe('Hi\n\nFrom PRB-1:\nReboot');
    expect(appendWorkaround('', '  ', 'From PRB-1:')).toBe('');
    expect(appendWorkaround('x'.repeat(4999), 'Workaround')).toHaveLength(5000);
  });
});

describe('ticket abilities', () => {
  const none = { manage: false, isOwner: false, open: true, canMove: false };
  const all = {
    comment: true,
    internalComment: true,
    assign: true,
    setPriority: true,
    transition: true,
    move: true,
    markDuplicate: true,
  };
  it('prefers the server abilities over global permissions', () => {
    const resolved = resolveAbilities(
      { ...all, assign: false, internalComment: false },
      { ...none, manage: true },
    );
    expect(resolved.assign).toBe(false);
    expect(resolved.composer).toBe(true);
    expect(resolved.canChooseKind).toBe(false);
  });
  it('shows no composer when neither kind is allowed', () => {
    const resolved = resolveAbilities(
      { ...all, comment: false, internalComment: false },
      { ...none, manage: true },
    );
    expect(resolved.composer).toBe(false);
  });
  it('falls back to permissions only when abilities are absent', () => {
    expect(resolveAbilities(undefined, { ...none, manage: true }).internalComment).toBe(true);
    const owner = resolveAbilities(undefined, { ...none, isOwner: true });
    expect(owner.comment).toBe(true);
    expect(owner.internalComment).toBe(false);
    expect(resolveAbilities(undefined, { ...none, manage: true, open: false }).composer).toBe(
      false,
    );
  });
  it('forces the only allowed comment kind', () => {
    expect(effectiveCommentKind({ comment: false, internalComment: true }, false)).toBe('internal');
    expect(effectiveCommentKind({ comment: true, internalComment: false }, true)).toBe('reply');
    expect(effectiveCommentKind({ comment: true, internalComment: true }, true)).toBe('internal');
  });
  it('offers the location correction only when the server says so', () => {
    const fallback = { manage: true, isOwner: false, open: true, canMove: false };
    expect(resolveAbilities(undefined, fallback).setLocation).toBe(false);
    const base = resolveAbilities(undefined, fallback);
    expect(resolveAbilities({ ...base, setLocation: true }, fallback).setLocation).toBe(true);
  });
});
