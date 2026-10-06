import { expect, it } from 'vitest';
import { mergeFilterQuery } from './filterQuery';
it('preserves context and replaces only owned filter keys', () => {
  expect(
    mergeFilterQuery('?deviceId=abc&status=open', { status: 'closed', severity: 'high' }),
  ).toBe('?deviceId=abc&status=closed&severity=high');
});
it('clears inactive filters without leaving empty query markers', () => {
  expect(
    mergeFilterQuery('?status=open&mine=true&priority=high', {
      status: '',
      mine: false,
      priority: null,
    }),
  ).toBe('');
});
it('encodes searches and boolean filters consistently', () => {
  expect(mergeFilterQuery('', { q: 'R&D + Berlin', mine: true })).toBe(
    '?q=R%26D+%2B+Berlin&mine=true',
  );
});
