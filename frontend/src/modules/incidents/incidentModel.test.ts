import { describe, expect, it } from 'vitest';
import { nextUpdateState } from './incidentModel';

describe('nextUpdateState', () => {
  const now = new Date('2026-10-10T12:00:00Z');
  it('classifies the promised update time', () => {
    expect(nextUpdateState(null, now)).toBe('none');
    expect(nextUpdateState('garbage', now)).toBe('none');
    expect(nextUpdateState('2026-10-10T13:00:00Z', now)).toBe('upcoming');
    expect(nextUpdateState('2026-10-10T11:00:00Z', now)).toBe('overdue');
  });
});
