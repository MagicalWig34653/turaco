import { describe, expect, it } from 'vitest';
import { answerText } from './RequestDetail';

describe('reference answer snapshots', () => {
  it('shows the submitted asset and location labels', () => {
    expect(
      answerText(
        { key: 'device', type: 'asset', label: 'Device' },
        { id: 'a1', name: 'Monitor', reference: 'AST-7' },
        {},
        'Yes',
        'No',
      ),
    ).toBe('Monitor · AST-7');
    expect(
      answerText(
        { key: 'where', type: 'location', label: 'Where' },
        { id: 'l1', name: 'Ward 3' },
        {},
        'Yes',
        'No',
      ),
    ).toBe('Ward 3');
  });
});
