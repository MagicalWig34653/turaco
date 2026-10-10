import { describe, expect, it } from 'vitest';
import { appendUnique } from './useAsync';

describe('appendUnique', () => {
  it('drops rows whose id is already loaded', () => {
    const merged = appendUnique([{ id: 'a' }, { id: 'b' }], [{ id: 'b' }, { id: 'c' }]);
    expect(merged.map((row) => row.id)).toEqual(['a', 'b', 'c']);
  });

  it('adds nothing when the same page arrives twice', () => {
    const page = [{ id: 'x' }, { id: 'y' }];
    expect(appendUnique(page, page)).toEqual(page);
  });

  it('keeps rows without a string id', () => {
    expect(appendUnique([1, 2], [2, 3])).toEqual([1, 2, 2, 3]);
  });
});
