import { describe, expect, it } from 'vitest';
import {
  describeImpactInText,
  duplicateCandidates,
  impactFields,
  mergeDevices,
  withDeviceNote,
} from './reportModel';

describe('impact signal', () => {
  it('sends nothing when no impact is chosen and flags patient care explicitly', () => {
    expect(impactFields('')).toEqual({});
    expect(impactFields('blocked')).toEqual({ impact: 'blocked' });
    expect(impactFields('patient_care')).toEqual({ impact: 'patient_care', patientImpact: true });
  });
  it('writes the impact into the text as fallback', () => {
    expect(describeImpactInText('Printer jams', 'Patient care affected')).toBe(
      '[Patient care affected]\nPrinter jams',
    );
    expect(describeImpactInText('  ', 'Blocked')).toBe('[Blocked]');
  });
});

describe('device note', () => {
  it('appends a free-text device only when given', () => {
    expect(withDeviceNote('Text', 'Device', '')).toBe('Text');
    expect(withDeviceNote('Text', 'Device', ' Printer ER ')).toBe('Text\n\nDevice: Printer ER');
    expect(withDeviceNote('', 'Device', 'Printer')).toBe('Device: Printer');
  });
});

describe('duplicate hint', () => {
  const mine = [
    { id: '1', reference: 'TKT-1', title: 'Drucker geht nicht', status: 'new' },
    { id: '2', reference: 'TKT-2', title: 'Drucker geht nicht', status: 'closed' },
    { id: '3', reference: 'TKT-3', title: 'Anderes', status: 'open' },
  ];
  it('finds identical open tickets ignoring case and spacing', () => {
    expect(duplicateCandidates('  drucker  GEHT nicht ', mine).map((t) => t.id)).toEqual(['1']);
  });
  it('ignores short titles and finished tickets', () => {
    expect(duplicateCandidates('abc', mine)).toEqual([]);
    expect(duplicateCandidates('Anderes', mine).map((t) => t.id)).toEqual(['3']);
  });
});

describe('mergeDevices', () => {
  it('drops shared devices already offered as own', () => {
    expect(mergeDevices([{ id: 'a' }], [{ id: 'a' }, { id: 'b' }]).shared).toEqual([{ id: 'b' }]);
  });
});
