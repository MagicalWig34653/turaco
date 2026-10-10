import { describe, expect, it } from 'vitest';
import { peoplePresets, presetState } from './peoplePresets';

describe('people presets', () => {
  it('starts "everyone" without a filter', () => {
    expect(presetState('all').filter.root).toBeUndefined();
  });
  it('builds ordinary single-condition filters the workbench understands', () => {
    expect(presetState('active').filter.root).toEqual({
      type: 'condition',
      field: 'status',
      op: 'equals',
      value: 'active',
    });
    expect(presetState('local').filter.root).toEqual({
      type: 'condition',
      field: 'source',
      op: 'equals',
      value: 'local',
    });
    expect(presetState('inactive').filter.root).toEqual({
      type: 'condition',
      field: 'status',
      op: 'in',
      value: ['inactive', 'departed'],
    });
    expect(presetState('noDepartment').filter.root).toEqual({
      type: 'condition',
      field: 'department',
      op: 'is_empty',
    });
  });
  it('lists every preset once', () => {
    expect(new Set(peoplePresets).size).toBe(peoplePresets.length);
  });
});
