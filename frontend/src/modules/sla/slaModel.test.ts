import { describe, expect, it } from 'vitest';
import { deserialize } from '../../platform/ui/query/filterModel';
import {
  defaultPeriod,
  periodError,
  relativeDue,
  reportLink,
  slaTone,
  splitDuration,
  stateLink,
  type ReportGroup,
} from './slaModel';

const queryOf = (link: string) => {
  const [path, search] = link.split('?');
  return {
    path,
    filter: JSON.stringify(deserialize(new URLSearchParams(search).get('query') ?? '')),
  };
};

describe('relativeDue', () => {
  const now = new Date('2026-10-12T12:00:00Z');
  it('picks the coarsest whole unit and marks the past', () => {
    expect(relativeDue('2026-10-12T12:30:00Z', now)).toEqual({
      unit: 'minutes',
      count: 30,
      past: false,
    });
    expect(relativeDue('2026-10-12T09:00:00Z', now)).toEqual({
      unit: 'hours',
      count: 3,
      past: true,
    });
    expect(relativeDue('2026-10-16T12:00:00Z', now)).toEqual({
      unit: 'days',
      count: 4,
      past: false,
    });
    expect(relativeDue('2026-10-12T12:00:10Z', now)).toEqual({
      unit: 'minutes',
      count: 1,
      past: false,
    });
  });
  it('ignores missing and broken times', () => {
    expect(relativeDue(null, now)).toBeUndefined();
    expect(relativeDue('nope', now)).toBeUndefined();
  });
});

describe('slaTone', () => {
  it('maps the states to tones', () => {
    expect(slaTone('breached')).toBe('danger');
    expect(slaTone('at_risk')).toBe('warning');
    expect(slaTone('met')).toBe('success');
    expect(slaTone('none')).toBe('neutral');
  });
});

describe('report links', () => {
  const queue: ReportGroup = {
    key: 'q-1',
    queue: { id: 'q-1', key: 'a', prefix: 'A', name: 'Desk' },
    kinds: [],
  };
  it('filters by outcome, Queue and the resolved period for the resolution clock', () => {
    const link = queryOf(
      reportLink(
        { from: '2026-01-01', to: '2026-01-31', groupBy: 'queue' },
        queue,
        'resolution',
        'breached',
      ),
    );
    expect(link.path).toBe('/service-desk');
    expect(link.filter).toContain('sla_resolution_outcome');
    expect(link.filter).toContain('breached');
    expect(link.filter).toContain('q-1');
    expect(link.filter).toContain('resolved_at');
    expect(link.filter).toContain('2026-01-31');
  });
  it('carries no date for the first-response clock and clips a month to the period', () => {
    const response = queryOf(
      reportLink(
        { from: '2026-01-01', to: '2026-01-31', groupBy: 'queue' },
        queue,
        'first_response',
        'met',
      ),
    );
    expect(response.filter).toContain('sla_response_outcome');
    expect(response.filter).not.toContain('resolved_at');
    const month = queryOf(
      reportLink(
        { from: '2026-01-10', to: '2026-03-05', groupBy: 'month' },
        { key: '2026-02', kinds: [] },
        'resolution',
        'breached',
      ),
    );
    expect(month.filter).toContain('2026-02-01');
    expect(month.filter).toContain('2026-02-28');
    const first = queryOf(
      reportLink(
        { from: '2026-01-10', to: '2026-03-05', groupBy: 'month' },
        { key: '2026-01', kinds: [] },
        'resolution',
        'breached',
      ),
    );
    expect(first.filter).toContain('2026-01-10');
    expect(first.filter).toContain('2026-01-31');
  });
  it('never offers a per-person grouping link', () => {
    const link = queryOf(
      reportLink(
        { from: '2026-01-01', to: '2026-01-31', groupBy: 'priority' },
        { key: 'urgent', kinds: [] },
        'resolution',
        'breached',
      ),
    );
    expect(link.filter).toContain('priority');
    expect(link.filter).not.toContain('assignee');
  });
});

describe('stateLink', () => {
  it('limits to open tickets and optionally one Queue', () => {
    const link = queryOf(stateLink(['at_risk', 'breached'], 'q-9'));
    expect(link.filter).toContain('in_progress');
    expect(link.filter).toContain('sla_state');
    expect(link.filter).toContain('q-9');
  });
});

describe('period helpers', () => {
  it('defaults to the last 30 UTC days and validates the range', () => {
    expect(defaultPeriod(new Date('2026-10-12T05:00:00Z'))).toEqual({
      from: '2026-09-13',
      to: '2026-10-12',
    });
    expect(periodError('2026-01-02', '2026-01-01')).toBe('order');
    expect(periodError('', '2026-01-01')).toBe('order');
    expect(periodError('2025-01-01', '2026-01-02')).toBe('length');
    expect(periodError('2025-01-01', '2026-01-01')).toBeUndefined();
  });
  it('splits seconds into hours and minutes', () => {
    expect(splitDuration(7500)).toEqual({ hours: 2, minutes: 5 });
    expect(splitDuration(20)).toEqual({ hours: 0, minutes: 0 });
  });
});
