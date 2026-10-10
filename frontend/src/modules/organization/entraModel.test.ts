import { describe, expect, it } from 'vitest';
import type { PersonRow } from './adminTypes';
import {
  entraActions,
  entraFormErrors,
  entraTenantOf,
  isEntraIdentity,
  isGuid,
  normalizeGuid,
} from './entraModel';

const can =
  (...granted: string[]) =>
  (permission: string) =>
    granted.includes(permission);

const person = (
  over: Partial<Pick<PersonRow, 'id' | 'source' | 'accountKind' | 'status'>> = {},
): Pick<PersonRow, 'id' | 'source' | 'accountKind' | 'status'> => ({
  id: 'u1',
  source: 'directory',
  accountKind: 'employee',
  status: 'active',
  ...over,
});

describe('guid input', () => {
  it('accepts a GUID in any case with surrounding spaces and rejects everything else', () => {
    expect(isGuid(' 0F0F0F0F-0000-1111-2222-333333333333 ')).toBe(true);
    expect(normalizeGuid(' 0F0F0F0F-0000-1111-2222-333333333333 ')).toBe(
      '0f0f0f0f-0000-1111-2222-333333333333',
    );
    for (const bad of [
      '',
      'anna@example.org',
      'common',
      '0f0f0f0f-0000-1111-2222',
      '{0f0f0f0f-0000-1111-2222-333333333333}',
    ]) {
      expect(isGuid(bad)).toBe(false);
    }
  });

  it('reports required and invalid per field', () => {
    expect(entraFormErrors('', '')).toEqual({ tenant: 'required', object: 'required' });
    expect(entraFormErrors('common', 'x')).toEqual({ tenant: 'invalid', object: 'invalid' });
    expect(
      entraFormErrors(
        '11111111-2222-3333-4444-555555555555',
        '0f0f0f0f-0000-1111-2222-333333333333',
      ),
    ).toEqual({});
  });
});

describe('entra identities', () => {
  it('recognizes Entra provider keys', () => {
    expect(isEntraIdentity({ providerKey: 'entra:11111111-2222-3333-4444-555555555555' })).toBe(
      true,
    );
    expect(isEntraIdentity({ providerKey: 'ad' })).toBe(false);
    expect(entraTenantOf({ providerKey: 'entra:abc' })).toBe('abc');
  });
});

describe('entraActions', () => {
  it('offers link and unlink to platform administrators only', () => {
    expect(entraActions(person(), can('platform.admin'), 'me')).toEqual({
      link: true,
      unlink: true,
    });
    expect(entraActions(person(), can('organization.users.manage'), 'me')).toEqual({
      link: false,
      unlink: false,
    });
  });

  it('never offers anything for oneself, the emergency account or external accounts', () => {
    const admin = can('platform.admin');
    expect(entraActions(person({ id: 'me' }), admin, 'me')).toEqual({ link: false, unlink: false });
    expect(entraActions(person({ source: 'emergency' }), admin, 'me')).toEqual({
      link: false,
      unlink: false,
    });
    expect(entraActions(person({ accountKind: 'external' }), admin, 'me')).toEqual({
      link: false,
      unlink: false,
    });
  });

  it('allows removing the link of an inactive account but not creating one', () => {
    expect(entraActions(person({ status: 'inactive' }), can('platform.admin'), 'me')).toEqual({
      link: false,
      unlink: true,
    });
  });
});
