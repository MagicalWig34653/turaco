import { describe, expect, it } from 'vitest';
import { buildElevation } from './elevation';
import type { Placement } from './types';
const p = (
  id: string,
  face: 'front' | 'rear',
  uPosition: number,
  heightU: number,
  removedAt: string | null = null,
) => ({ id, face, uPosition, heightU, removedAt }) as Placement;
describe('buildElevation', () => {
  it('orders units from top to bottom and marks each occupied face', () => {
    const rows = buildElevation(4, [
      p('a', 'front', 2, 2),
      p('b', 'rear', 3, 1),
      p('old', 'front', 1, 1, '2026-01-01'),
    ]);
    expect(rows.map((row) => row.unit)).toEqual([4, 3, 2, 1]);
    expect(rows[1]?.front?.id).toBe('a');
    expect(rows[1]?.rear?.id).toBe('b');
    expect(rows[2]?.front?.id).toBe('a');
    expect(rows[3]?.front).toBeUndefined();
  });
});
