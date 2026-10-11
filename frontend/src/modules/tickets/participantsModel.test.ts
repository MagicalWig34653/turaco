import { describe, expect, it } from 'vitest';
import { MAX_PARTICIPANT_DAYS, participantExpiry } from './participantsModel';

describe('participantExpiry', () => {
  const now = new Date(2026, 9, 11, 12, 0, 0);

  it('ends the sharing at the end of the chosen local day', () => {
    const value = participantExpiry('2026-10-20', now);
    expect(value).toBeDefined();
    const end = new Date(value ?? '');
    expect([
      end.getFullYear(),
      end.getMonth(),
      end.getDate(),
      end.getHours(),
      end.getMinutes(),
    ]).toEqual([2026, 9, 20, 23, 59]);
  });

  it('refuses an empty, malformed or past day', () => {
    expect(participantExpiry('', now)).toBeUndefined();
    expect(participantExpiry('tomorrow', now)).toBeUndefined();
    expect(participantExpiry('2026-10-10', now)).toBeUndefined();
  });

  it('never goes beyond the server limit', () => {
    const value = participantExpiry('2030-01-01', now);
    expect(new Date(value ?? '').getTime()).toBeLessThan(
      now.getTime() + MAX_PARTICIPANT_DAYS * 86_400_000,
    );
  });
});
