import { describe, expect, it } from 'vitest';
import { relativeDate, sortRows } from './tableModel';

describe('loaded table sorting', () => {
  it('sorts references naturally without mutating the source', () => {
    const source = ['TKT-10', 'TKT-2', 'TKT-1'];
    expect(sortRows(source, (x) => x, 'asc', 'en')).toEqual(['TKT-1', 'TKT-2', 'TKT-10']);
    expect(source[0]).toBe('TKT-10');
  });
  it('keeps missing values last even descending and preserves equal order', () => {
    const rows = [
      { id: 1, n: null },
      { id: 2, n: 3 },
      { id: 3, n: 3 },
      { id: 4, n: 8 },
    ];
    expect(sortRows(rows, (x) => x.n, 'desc', 'de').map((x) => x.id)).toEqual([4, 2, 3, 1]);
  });
});
describe('relative dates', () => {
  const now = Date.parse('2026-10-06T12:00:00Z');
  it('supports past and future and both locales', () => {
    expect(relativeDate('2026-10-06T10:00:00Z', 'en', now)).toBe('2 hours ago');
    expect(relativeDate('2026-10-06T14:00:00Z', 'de', now)).toBe('in 2 Stunden');
  });
  it('does not lose invalid source data', () =>
    expect(relativeDate('unknown', 'en', now)).toBe('unknown'));
});
