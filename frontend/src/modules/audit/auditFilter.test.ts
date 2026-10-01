import { describe, expect, it } from 'vitest';
import { emptyForm, toAuditFilter } from './auditFilter';

describe('toAuditFilter', () => {
  it('omits empty fields and trims values', () => {
    expect(toAuditFilter(emptyForm)).toEqual({});
    expect(
      toAuditFilter({ ...emptyForm, actionPrefix: ' authorization. ', actorId: 'u1' }),
    ).toEqual({
      actionPrefix: 'authorization.',
      actorId: 'u1',
    });
  });
  it('converts date inputs to RFC 3339', () => {
    const filter = toAuditFilter({ ...emptyForm, from: '2026-01-01T00:00' });
    expect(filter.from).toMatch(/Z$/);
    expect(filter.to).toBeUndefined();
  });
});
