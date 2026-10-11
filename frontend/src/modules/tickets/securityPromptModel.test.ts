import { describe, expect, it } from 'vitest';
import { dataProtectionDueValue, shouldSuggestDataProtectionTask } from './securityPromptModel';
import type { TicketDetail } from './types';

describe('security ticket data protection prompt', () => {
  const ticket = {
    queue: { key: 'security' },
    abilities: { scheduleCallback: true },
  } as TicketDetail;
  it('proposes only on a security Queue with callback authority and no matching Task', () => {
    expect(shouldSuggestDataProtectionTask(ticket, [], ['Data protection assessment'])).toBe(true);
    expect(
      shouldSuggestDataProtectionTask(
        ticket,
        [{ title: 'Data protection assessment' }],
        ['Data protection assessment'],
      ),
    ).toBe(false);
    expect(
      shouldSuggestDataProtectionTask(
        { ...ticket, queue: null },
        [],
        ['Data protection assessment'],
      ),
    ).toBe(false);
    expect(
      shouldSuggestDataProtectionTask(
        { ...ticket, abilities: undefined } as unknown as Parameters<
          typeof shouldSuggestDataProtectionTask
        >[0],
        [],
        ['Data protection assessment'],
      ),
    ).toBe(false);
    expect(shouldSuggestDataProtectionTask(ticket, undefined, ['Data protection assessment'])).toBe(
      false,
    );
  });
  it('leaves an expired suggested due time blank for staff to choose', () => {
    expect(dataProtectionDueValue('2026-01-01T00:00:00Z', Date.parse('2026-01-05T00:00:00Z'))).toBe(
      '',
    );
    expect(dataProtectionDueValue('invalid')).toBe('');
  });
});
