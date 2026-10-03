import { describe, expect, it } from 'vitest';
import { diffBadge, historyChanges, orderedCounts, visibleGroupName } from './viewHelpers';

describe('management view helpers', () => {
  it('does not display an external ID when the API redacts a group name', () => {
    expect(visibleGroupName({ externalId: 'secret-id', name: null, redacted: true })).toBeNull();
    expect(
      visibleGroupName({ externalId: 'secret-id', name: 'Unexpected', redacted: true }),
    ).toBeNull();
    expect(
      visibleGroupName({ externalId: 'visible-id', name: 'Engineering', redacted: false }),
    ).toBe('Engineering');
  });

  it('keeps result order and shows zero for absent counts', () => {
    expect(
      orderedCounts(['applicable', 'excluded', 'unknown'], { unknown: 2, applicable: 3 }),
    ).toEqual([
      { key: 'applicable', count: 3 },
      { key: 'excluded', count: 0 },
      { key: 'unknown', count: 2 },
    ]);
  });
});

describe('history and diff helpers', () => {
  it('marks uncertain comparisons without losing their class', () => {
    expect(diffBadge('same', true)).toBe('uncertain');
    expect(diffBadge('only_left', false)).toBe('only_left');
  });
  it('keeps only documented changed fields', () => {
    expect(historyChanges(['target', 'noise', 'filter'])).toEqual(['target', 'filter']);
  });
});
