import { describe, expect, it } from 'vitest';
import { hasCondition, moveRule, parseKeywords } from './routingModel';

describe('routing model', () => {
  it('parses keywords distinct and bounded', () => {
    expect(parseKeywords('Heizung, heizung\n Lüftung ,,')).toEqual(['Heizung', 'Lüftung']);
    expect(parseKeywords(Array.from({ length: 15 }, (_, i) => `w${i}`).join(','))).toHaveLength(10);
    expect(parseKeywords('  ')).toEqual([]);
  });
  it('requires a condition', () => {
    expect(hasCondition({ keywords: [], impact: '', patientImpact: undefined })).toBe(false);
    expect(hasCondition({ keywords: ['a'], impact: '', patientImpact: undefined })).toBe(true);
    expect(hasCondition({ keywords: [], impact: 'blocked', patientImpact: undefined })).toBe(true);
    expect(hasCondition({ keywords: [], impact: '', patientImpact: false })).toBe(true);
  });
  it('moves a rule by one place and stops at the edges', () => {
    expect(moveRule(['a', 'b', 'c'], 'b', -1)).toEqual(['b', 'a', 'c']);
    expect(moveRule(['a', 'b', 'c'], 'b', 1)).toEqual(['a', 'c', 'b']);
    expect(moveRule(['a', 'b', 'c'], 'a', -1)).toEqual(['a', 'b', 'c']);
    expect(moveRule(['a', 'b', 'c'], 'c', 1)).toEqual(['a', 'b', 'c']);
    expect(moveRule(['a'], 'x', 1)).toEqual(['a']);
  });
});
