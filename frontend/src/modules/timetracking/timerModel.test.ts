import { describe, expect, it } from 'vitest';
import { elapsedMinutes, isStaleTimer } from './timerModel';

describe('timer model', () => {
  const start = '2026-10-12T08:00:00Z';
  const at = (offsetMs: number) => Date.parse(start) + offsetMs;
  it('rounds elapsed time up to whole minutes with a minimum of one', () => {
    expect(elapsedMinutes(start, at(0))).toBe(1);
    expect(elapsedMinutes(start, at(61_000))).toBe(2);
    expect(elapsedMinutes(start, at(3_600_000))).toBe(60);
    expect(elapsedMinutes('invalid', at(1000))).toBe(1);
  });
  it('treats a timer older than 12 hours as stale', () => {
    expect(isStaleTimer(start, at(12 * 3_600_000))).toBe(false);
    expect(isStaleTimer(start, at(12 * 3_600_000 + 1))).toBe(true);
  });
});
