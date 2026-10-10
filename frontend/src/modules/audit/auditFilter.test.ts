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

describe('toAuditFilter new filters', () => {
  it('keeps module, actor kind, system actor and via, and drops invalid values', () => {
    expect(
      toAuditFilter({
        ...emptyForm,
        module: 'platform',
        actorKind: 'system',
        systemActor: 'cli',
        via: 'ai',
      }),
    ).toEqual({ module: 'platform', actorKind: 'system', systemActor: 'cli', via: 'ai' });
    expect(toAuditFilter({ ...emptyForm, actorKind: 'robot', via: 'x' })).toEqual({});
  });
  it('lets an explicit action prefix win over module', () => {
    expect(
      toAuditFilter({ ...emptyForm, module: 'platform', actionPrefix: 'platform.setup.' }),
    ).toEqual({
      actionPrefix: 'platform.setup.',
    });
  });
});
