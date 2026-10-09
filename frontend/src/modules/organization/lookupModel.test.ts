import { describe, expect, it } from 'vitest';
import { lookupState } from './lookupModel';

describe('lookupState', () => {
  it('does not search before three characters and explains why', () => {
    expect(lookupState('')).toEqual({ search: false, tooShort: false });
    expect(lookupState('Br')).toEqual({ search: false, tooShort: true });
    expect(lookupState(' Bra ')).toEqual({ search: true, tooShort: false });
  });
  it('counts characters, not bytes', () => {
    expect(lookupState('Öz')).toEqual({ search: false, tooShort: true });
    expect(lookupState('Özü')).toEqual({ search: true, tooShort: false });
  });
});
