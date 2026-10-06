import { describe, expect, it } from 'vitest';
import { metricState } from './Workspace';

describe('metricState', () => {
  it('keeps the requested tone for non-zero counts', () => {
    expect(metricState(2, 'danger', 'Past due', 'Nothing overdue')).toEqual({
      tone: 'danger',
      caption: 'Past due',
    });
  });
  it('turns an alarming zero into calm success wording', () => {
    expect(metricState(0, 'danger', 'Past due', 'Nothing overdue')).toEqual({
      tone: 'success',
      caption: 'Nothing overdue',
    });
  });
  it('falls back to neutral without a zero caption', () => {
    expect(metricState(0, 'warning', 'Make these a priority')).toEqual({
      tone: 'neutral',
      caption: 'Make these a priority',
    });
  });
  it('leaves informational zeros unchanged', () => {
    expect(metricState(0, 'info', 'Loaded')).toEqual({ tone: 'info', caption: 'Loaded' });
  });
});
