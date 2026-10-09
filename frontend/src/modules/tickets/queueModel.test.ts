import { describe, expect, it } from 'vitest';
import {
  abilitiesOf,
  addSubject,
  aliasList,
  canOfferMove,
  grantsToMatrix,
  intakeChoice,
  matrixToGrants,
  moveTargets,
  previewReference,
  queueChoiceLabel,
  removeSubject,
  sameMatrix,
  setAccess,
  setCreate,
  suggestKey,
  suggestPrefix,
  ticketQueueName,
  validateQueueForm,
} from './queueModel';
import type { TicketQueue } from './types';

const queue = (id: string, extra: Partial<TicketQueue> = {}): TicketQueue => ({
  id,
  key: id,
  prefix: id.toUpperCase().slice(0, 3),
  name: `Queue ${id}`,
  description: '',
  status: 'active',
  visibility: 'public',
  isDefault: false,
  version: 1,
  level: 'work',
  canCreate: true,
  ...extra,
});

describe('queue form validation', () => {
  it('accepts valid keys and prefixes and rejects the rest', () => {
    expect(validateQueueForm({ key: 'hr-desk', prefix: 'HR', name: 'HR' })).toEqual({});
    expect(validateQueueForm({ key: 'HR', prefix: 'hr', name: ' ' })).toEqual({
      key: 'keyInvalid',
      prefix: 'prefixInvalid',
      name: 'nameRequired',
    });
    expect(validateQueueForm({ key: 'a', prefix: 'A', name: 'x' }).key).toBe('keyInvalid');
    expect(validateQueueForm({ key: '1abc', prefix: '1AB', name: 'x' })).toMatchObject({
      key: 'keyInvalid',
      prefix: 'prefixInvalid',
    });
    expect(validateQueueForm({ key: 'ok', prefix: 'ABCDEFGHI', name: 'x' }).prefix).toBe(
      'prefixInvalid',
    );
    expect(validateQueueForm({ key: 'ok', prefix: 'OK', name: 'n'.repeat(81) }).name).toBe(
      'nameTooLong',
    );
  });
  it('suggests a key and prefix from a name', () => {
    expect(suggestKey('Human Résources & Payroll')).toBe('human-resources-payroll');
    expect(suggestKey('  42 Facility')).toBe('facility');
    expect(suggestPrefix('Human Resources')).toBe('HUMA');
    expect(suggestPrefix('9 to 5')).toBe('TO5');
  });
  it('previews the next reference', () => {
    expect(previewReference('HR', 4)).toBe('HR-0001');
    expect(previewReference('', 3)).toBe('…-001');
  });
});

describe('intake choice', () => {
  it('lets the server decide without usable Queues', () => {
    expect(intakeChoice(undefined)).toEqual({ mode: 'default' });
    expect(intakeChoice([queue('a', { canCreate: false })])).toEqual({ mode: 'default' });
    expect(intakeChoice([queue('a', { status: 'archived' })])).toEqual({ mode: 'default' });
  });
  it('uses a single Queue silently', () => {
    const only = queue('a');
    expect(intakeChoice([only])).toEqual({ mode: 'single', queue: only });
  });
  it('asks only when more than one Queue is on offer, starting at the intake Queue', () => {
    const result = intakeChoice([queue('a'), queue('b', { isDefault: true }), queue('c')]);
    expect(result.mode).toBe('choose');
    if (result.mode === 'choose') {
      expect(result.queues.map((q) => q.id)).toEqual(['a', 'b', 'c']);
      expect(result.initial.id).toBe('b');
    }
    const noDefault = intakeChoice([queue('a'), queue('b')]);
    expect(noDefault.mode === 'choose' && noDefault.initial.id).toBe('a');
  });
  it('labels Queues in plain language for employees and with the prefix for staff', () => {
    const q = queue('hr', { name: 'HR desk', publicLabel: 'Questions about work contracts' });
    expect(queueChoiceLabel(q, false)).toBe('Questions about work contracts');
    expect(queueChoiceLabel(queue('x', { name: 'Facility' }), false)).toBe('Facility');
    expect(queueChoiceLabel(q, true)).toBe('HR desk (HR)');
  });
});

describe('ticket queue display and move', () => {
  it('names the Queue or falls back to the neutral desk label', () => {
    expect(ticketQueueName({ queue: { id: '1', key: 'it', prefix: 'TKT', name: 'IT' } })).toBe(
      'IT',
    );
    expect(ticketQueueName({ queueLabel: 'Handled by Facility' })).toBe('Handled by Facility');
    expect(ticketQueueName({})).toBeUndefined();
  });
  it('lists move targets other than the current, active Queue the caller can raise into', () => {
    const all = [
      queue('a'),
      queue('b'),
      queue('c', { canCreate: false }),
      queue('d', { status: 'archived' }),
    ];
    expect(moveTargets(all, 'a').map((q) => q.id)).toEqual(['b']);
    expect(moveTargets(undefined, 'a')).toEqual([]);
  });
  it('offers a move only for unfinished tickets', () => {
    expect(canOfferMove({ status: 'open' })).toBe(true);
    expect(canOfferMove({ status: 'waiting' })).toBe(true);
    expect(canOfferMove({ status: 'closed' })).toBe(false);
    expect(canOfferMove({ status: 'cancelled' })).toBe(false);
    expect(canOfferMove({ status: 'resolved' })).toBe(false);
  });
  it('lists aliases without the current reference', () => {
    expect(aliasList({ reference: 'HR-0003', aliases: ['TKT-000012', 'HR-0003'] })).toEqual([
      'TKT-000012',
    ]);
    expect(aliasList({ reference: 'HR-0003' })).toEqual([]);
  });
});

describe('grants matrix', () => {
  const user = { subjectType: 'user' as const, subjectId: 'u1' };
  const team = { subjectType: 'team' as const, subjectId: 't1' };

  it('folds rows per subject into the strongest access plus create', () => {
    expect(
      grantsToMatrix([
        { ...user, level: 'view' },
        { ...user, level: 'work' },
        { ...user, level: 'create' },
        { ...team, level: 'create' },
      ]),
    ).toEqual([
      { ...user, access: 'work', create: true },
      { ...team, access: '', create: true },
    ]);
  });
  it('writes the minimal grant rows back', () => {
    expect(
      matrixToGrants([
        { ...user, access: 'work', create: true },
        { ...team, access: 'view', create: false },
        { subjectType: 'role', subjectId: 'r1', access: '', create: false },
      ]),
    ).toEqual([
      { ...user, level: 'work' },
      { ...user, level: 'create' },
      { ...team, level: 'view' },
    ]);
  });
  it('adds a subject once with read access and removes it again', () => {
    const rows = addSubject(addSubject([], user), user);
    expect(rows).toEqual([{ ...user, access: 'view', create: false }]);
    expect(removeSubject(rows, user)).toEqual([]);
  });
  it('keeps work and view consistent', () => {
    let rows = addSubject([], user);
    rows = setAccess(rows, user, 'work', true);
    expect(rows[0]?.access).toBe('work');
    rows = setAccess(rows, user, 'view', true);
    expect(rows[0]?.access).toBe('work');
    rows = setAccess(rows, user, 'work', false);
    expect(rows[0]?.access).toBe('view');
    rows = setAccess(rows, user, 'view', false);
    expect(rows[0]?.access).toBe('');
    rows = setCreate(rows, user, true);
    expect(rows[0]?.create).toBe(true);
    expect(
      setAccess([{ ...user, access: 'manage', create: false }], user, 'work', false)[0]?.access,
    ).toBe('manage');
  });
  it('derives read, work, internal comments, create and move abilities', () => {
    expect(abilitiesOf({ access: 'view', create: false })).toEqual({
      read: true,
      work: false,
      internalComments: false,
      create: false,
      moveOut: false,
      moveIn: false,
    });
    expect(abilitiesOf({ access: 'work', create: true })).toEqual({
      read: true,
      work: true,
      internalComments: true,
      create: true,
      moveOut: true,
      moveIn: true,
    });
    expect(abilitiesOf({ access: '', create: true })).toMatchObject({ read: false, moveIn: true });
  });
  it('compares matrices ignoring order and empty rows', () => {
    const a = [{ ...user, access: 'view' as const, create: false }];
    const b = [
      { ...team, access: '' as const, create: false },
      { ...user, access: 'view' as const, create: false },
    ];
    expect(sameMatrix(a, b)).toBe(true);
    expect(sameMatrix(a, [{ ...user, access: 'work' as const, create: false }])).toBe(false);
  });
});
