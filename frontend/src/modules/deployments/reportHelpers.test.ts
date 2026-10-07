import { describe, expect, it } from 'vitest';
import {
  clusterLabel,
  clusterShare,
  durationLabel,
  hasReport,
  reasonBars,
  rolloutPercent,
  rolloutStatusQuery,
  sharePercent,
} from './reportHelpers';

describe('hasReport', () => {
  it('is available from scheduled on', () => {
    for (const status of ['scheduled', 'running', 'paused', 'completed', 'failed']) {
      expect(hasReport(status, null)).toBe(true);
    }
    for (const status of ['draft', 'pending_approval', 'approved']) {
      expect(hasReport(status, null)).toBe(false);
    }
  });
  it('needs a schedule for a cancelled plan', () => {
    expect(hasReport('cancelled', null)).toBe(false);
    expect(hasReport('cancelled', '2026-10-01T10:00:00Z')).toBe(true);
  });
});

describe('durationLabel', () => {
  it('formats seconds, minutes and days', () => {
    expect(durationLabel(null)).toBeNull();
    expect(durationLabel(undefined)).toBeNull();
    expect(durationLabel(-1)).toBeNull();
    expect(durationLabel(42)).toBe('42 s');
    expect(durationLabel(600)).toBe('10 min');
    expect(durationLabel(90_000)).toBe('1 d 1 h');
  });
});

describe('shares', () => {
  it('rounds and guards empty totals', () => {
    expect(sharePercent(1, 3)).toBe(33);
    expect(sharePercent(2, 3)).toBe(67);
    expect(sharePercent(0, 5)).toBe(0);
    expect(sharePercent(5, 0)).toBe(0);
    expect(clusterShare({ failed: 6, total: 8 })).toBe(75);
    expect(rolloutPercent({ decided: 1, targetTotal: 4 })).toBe(25);
  });
});

describe('cluster and reason display', () => {
  it('labels a cluster with dimension and value', () => {
    expect(clusterLabel('Model', 'Latitude 7440')).toBe('Model: Latitude 7440');
  });
  it('sorts reasons and scales bars to the largest', () => {
    expect(
      reasonBars([
        { code: 'b', count: 2 },
        { code: 'a', count: 4 },
        { code: 'c', count: 0 },
        { code: 'd', count: 2 },
      ]),
    ).toEqual([
      { code: 'a', count: 4, percent: 100 },
      { code: 'b', count: 2, percent: 50 },
      { code: 'd', count: 2, percent: 50 },
    ]);
    expect(reasonBars([])).toEqual([]);
  });
});

describe('rolloutStatusQuery', () => {
  it('maps filters to status lists', () => {
    expect(rolloutStatusQuery('active')).toBe('');
    expect(rolloutStatusQuery('completed')).toBe('completed');
    expect(rolloutStatusQuery('ended')).toContain('failed');
  });
});
