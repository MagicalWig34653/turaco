import { describe, expect, it } from 'vitest';
import { previewState, stepText } from './approvalModel';

const step = (extra = {}) => ({
  index: 0,
  kind: 'manager',
  resolved: true,
  fallback: false,
  ...extra,
});

describe('approval preview', () => {
  it('tolerates a server without the preview', () => {
    expect(previewState(null)).toBe('unknown');
  });
  it('distinguishes no approval, a clear route and a blocked route', () => {
    expect(previewState({ approvalRequired: false, canSubmit: true, steps: [] })).toBe('none');
    expect(previewState({ approvalRequired: true, canSubmit: true, steps: [step()] })).toBe(
      'route',
    );
    expect(
      previewState({
        approvalRequired: true,
        canSubmit: false,
        steps: [step({ resolved: false })],
      }),
    ).toBe('blocked');
  });
  it('names the approver and marks fallbacks', () => {
    expect(stepText(step({ approverName: 'Anna' }))).toEqual({
      key: 'catalog.approval.step.manager',
      params: { name: 'Anna' },
    });
    expect(stepText(step({ fallback: true, approverName: 'Bernd' })).key).toBe(
      'catalog.approval.step.fallback',
    );
    expect(stepText(step({ resolved: false })).key).toBe('catalog.approval.step.unresolved');
  });
});
