import { describe, expect, it } from 'vitest';
import {
  actorView,
  defaultForm,
  diffRows,
  exportErrorKey,
  nameOf,
  osUserOf,
  rangeFormHours,
  rangeProblem,
  tooLargeCount,
  visibleMetadata,
} from './auditModel';
import type { AuditEvent } from './types';

const event = (patch: Partial<AuditEvent>): AuditEvent => ({
  id: 'e1',
  occurredAt: '2026-01-01T00:00:00Z',
  action: 'platform.x',
  targetType: 'user',
  targetId: 'u-123456789',
  correlationId: 'c1',
  metadata: {},
  ...patch,
});

describe('nameOf', () => {
  it('prefers the resolved name and falls back to the id', () => {
    expect(nameOf({ text: 'Ada' }, 'u1').name).toBe('Ada');
    expect(nameOf(undefined, 'u1')).toMatchObject({ name: 'u1', resolved: false });
  });
  it('keeps only the id suffix for a removed entity', () => {
    expect(nameOf({ gone: true }, 'u-123456789')).toMatchObject({ name: '456789', gone: true });
  });
});

describe('actorView', () => {
  it('distinguishes user, system, legacy metadata and unknown', () => {
    expect(actorView(event({ actorId: 'u1', actor: { text: 'Ada' } }))).toMatchObject({
      kind: 'user',
    });
    expect(actorView(event({ systemActor: 'cli' }))).toEqual({ kind: 'system', name: 'cli' });
    expect(actorView(event({ metadata: { actor: 'ops' } }))).toEqual({
      kind: 'metadata',
      name: 'ops',
    });
    expect(actorView(event({}))).toEqual({ kind: 'unknown' });
  });
});

describe('diffRows', () => {
  it('compares key by key and lists changes first', () => {
    const rows = diffRows(
      { a: 1, b: { c: 'x' }, keep: true },
      { a: 2, b: { c: 'x', d: 'y' }, keep: true },
    );
    expect(rows.map((r) => [r.path, r.kind])).toEqual([
      ['a', 'changed'],
      ['b.d', 'added'],
      ['b.c', 'same'],
      ['keep', 'same'],
    ]);
  });
  it('handles a missing before or after', () => {
    expect(diffRows(undefined, { a: 1 })).toEqual([
      { path: 'a', before: undefined, after: '1', kind: 'added' },
    ]);
    expect(diffRows({ a: 'x' }, null)[0]).toMatchObject({ kind: 'removed' });
    expect(diffRows(undefined, undefined)).toEqual([]);
  });
  it('compares arrays and scalars as values', () => {
    expect(diffRows({ p: ['a'] }, { p: ['a', 'b'] })[0]).toMatchObject({ kind: 'changed' });
    expect(diffRows('x', 'y')).toEqual([{ path: '', before: 'x', after: 'y', kind: 'changed' }]);
  });
});

describe('export range and errors', () => {
  it('requires a range of at most 92 days', () => {
    expect(rangeProblem({})).toBe('required');
    expect(rangeProblem({ from: '2026-01-02T00:00:00Z', to: '2026-01-01T00:00:00Z' })).toBe(
      'inverted',
    );
    expect(rangeProblem({ from: '2026-01-01T00:00:00Z', to: '2026-06-01T00:00:00Z' })).toBe(
      'tooLong',
    );
    expect(
      rangeProblem({ from: '2026-01-01T00:00:00Z', to: '2026-02-01T00:00:00Z' }),
    ).toBeUndefined();
  });
  it('maps server answers to messages', () => {
    expect(exportErrorKey({ status: 400, code: 'audit.range_required' })).toBe(
      'audit.export.error.rangeRequired',
    );
    expect(exportErrorKey({ status: 413, code: 'whatever' })).toBe('audit.export.error.tooLarge');
    expect(exportErrorKey({ status: 500, code: 'x' })).toBeUndefined();
    expect(tooLargeCount({ details: { count: 12000 } })).toBe(12000);
    expect(tooLargeCount({ details: 'x' })).toBeUndefined();
  });
  it('defaults to the last seven days', () => {
    const form = defaultForm(new Date('2026-03-10T12:00:00Z'));
    expect(form.from).not.toBe('');
    expect(form.to).not.toBe('');
  });
});

describe('quick ranges and osUser', () => {
  it('builds an hour based range', () => {
    const now = new Date('2026-10-10T12:00:00Z');
    const one = rangeFormHours(1, now);
    const day = rangeFormHours(24, now);
    expect(
      Date.parse(new Date(one.to).toISOString()) - Date.parse(new Date(one.from).toISOString()),
    ).toBe(3_600_000);
    expect(day.from < one.from).toBe(true);
  });

  it('hides osUser from metadata unless the viewer may export', () => {
    const meta = { actor: 'cli', osUser: 'root' };
    expect(visibleMetadata(meta, false)).toEqual({ actor: 'cli' });
    expect(visibleMetadata(meta, true)).toEqual(meta);
    expect(osUserOf(meta)).toBe('root');
    expect(osUserOf({})).toBeUndefined();
  });
});
