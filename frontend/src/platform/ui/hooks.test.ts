import { describe, expect, it } from 'vitest';
import { hasHorizontalOverflow } from './hooks';

describe('hasHorizontalOverflow', () => {
  it('reports overflow only when content is wider than the box', () => {
    expect(hasHorizontalOverflow({ scrollWidth: 1200, clientWidth: 1000 })).toBe(true);
    expect(hasHorizontalOverflow({ scrollWidth: 1000, clientWidth: 1000 })).toBe(false);
    expect(hasHorizontalOverflow({ scrollWidth: 1001, clientWidth: 1000 })).toBe(false);
  });
});
