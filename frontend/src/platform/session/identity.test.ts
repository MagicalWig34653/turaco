import { describe, expect, it } from 'vitest';
import { dayPart, greetingName, sessionDisplayName } from './identity';

const id = '01a105d1-93d0-7a95-b001-73afe47b7fea';

describe('session identity', () => {
  it('prefers the given name for greetings', () => {
    expect(greetingName({ userId: id, displayName: 'Lena Hoffmann', givenName: 'Lena' })).toBe(
      'Lena',
    );
  });
  it('keeps the full display name instead of cutting off the first word', () => {
    expect(greetingName({ userId: id, displayName: 'Development Admin' })).toBe(
      'Development Admin',
    );
  });
  it('never shows identifiers or blanks', () => {
    expect(greetingName({ userId: id })).toBeUndefined();
    expect(greetingName({ userId: id, displayName: '  ' })).toBeUndefined();
    expect(greetingName({ userId: id, displayName: id })).toBeUndefined();
    expect(sessionDisplayName({ userId: 'u1', displayName: 'u1' })).toBeUndefined();
    expect(sessionDisplayName(null)).toBeUndefined();
  });
  it('maps the hour to a part of the day', () => {
    expect(dayPart(new Date(2026, 9, 6, 8))).toBe('morning');
    expect(dayPart(new Date(2026, 9, 6, 13))).toBe('afternoon');
    expect(dayPart(new Date(2026, 9, 6, 21))).toBe('evening');
    expect(dayPart(new Date(2026, 9, 6, 3))).toBe('evening');
  });
});
