import { describe, expect, it } from 'vitest';
import {
  countdownParts,
  formatCountdown,
  gateChecklist,
  progressSegments,
  rateLabel,
  reasonCategory,
  reasonNeedsAttention,
  remainingSoak,
  ringActions,
  runActions,
  settledPercent,
  shouldPoll,
} from './runHelpers';
import type { ChangeWindow, DeploymentRing, RingProgress } from './types';

const ring = (patch: Partial<RingProgress> = {}): RingProgress => ({
  ringId: 'r1',
  ringRunId: 'run1',
  position: 1,
  name: 'Pilot',
  status: 'awaiting_promotion',
  statusReason: null,
  activatedAt: null,
  settledAt: '2026-01-01T00:00:00Z',
  awaitingSince: '2026-01-01T00:00:00Z',
  promotedAt: null,
  haltedAt: null,
  promotionApprovalStatus: null,
  approvalRequired: false,
  successThresholdPercent: 90,
  soakMinutes: 60,
  counts: {},
  freshSuccessful: 9,
  freshObserved: 10,
  successRatePercent: 90,
  soakRemainingSeconds: 0,
  nextGate: 'promotion',
  ...patch,
});
const planRing = { id: 'r1', changeId: null } as DeploymentRing;
const now = new Date('2026-06-01T12:00:00Z');
const state = (items: ReturnType<typeof gateChecklist>, id: string) =>
  items.find((item) => item.id === id)?.state;

describe('progress display math', () => {
  it('orders segments, drops zero counts and never exceeds 100 percent', () => {
    const segments = progressSegments({ failed: 1, successful: 1, pending: 1, expired: 0 });
    expect(segments.map((s) => s.state)).toEqual(['successful', 'pending', 'failed']);
    expect(segments.reduce((sum, s) => sum + s.percent, 0)).toBe(100);
    expect(progressSegments({})).toEqual([]);
    expect(progressSegments({ future_state: 2 })[0]?.state).toBe('future_state');
  });

  it('counts everything but open states as decided', () => {
    expect(settledPercent({ successful: 1, pending: 1, awaiting_observation: 2 })).toBe(25);
    expect(settledPercent({})).toBe(0);
  });

  it('formats rates and countdowns', () => {
    expect(rateLabel(null)).toBeNull();
    expect(rateLabel(87.456)).toBe('87.5%');
    expect(countdownParts(90_000)).toEqual({ days: 1, hours: 1, minutes: 0 });
    expect(formatCountdown(61)).toBe('2 min');
    expect(formatCountdown(0)).toBe('0 min');
    expect(formatCountdown(3 * 3600 + 5 * 60)).toBe('3 h 5 min');
    expect(remainingSoak(100, 0, 40_000)).toBe(60);
    expect(remainingSoak(10, 0, 40_000)).toBe(0);
  });

  it('polls only while the engine can still move the deployment', () => {
    expect(shouldPoll('running')).toBe(true);
    expect(shouldPoll('paused')).toBe(false);
    expect(shouldPoll('completed')).toBe(false);
  });
});

describe('gate checklist', () => {
  it('passes threshold, soak, approval and window when everything is met', () => {
    const open: Record<string, ChangeWindow> = {
      c1: {
        id: 'c1',
        status: 'approved',
        windowStart: '2026-06-01T00:00:00Z',
        windowEnd: '2026-06-02T00:00:00Z',
      },
    };
    const items = gateChecklist(ring(), { ...planRing, changeId: 'c1' }, open, now);
    expect(items.map((item) => item.state)).toEqual(['pass', 'pass', 'na', 'pass']);
  });

  it('explains what blocks', () => {
    const items = gateChecklist(
      ring({
        successRatePercent: 50,
        soakRemainingSeconds: 600,
        approvalRequired: true,
        promotionApprovalStatus: 'pending',
      }),
      { ...planRing, changeId: 'c1' },
      {},
      now,
    );
    expect(items.map((item) => [item.id, item.state, item.reason])).toEqual([
      ['threshold', 'fail', 'deployments.run.gate.threshold.below'],
      ['soak', 'fail', 'deployments.run.gate.soak.remaining'],
      ['approval', 'fail', 'deployments.run.gate.approval.pending'],
      ['window', 'fail', 'deployments.run.gate.window.unknown'],
    ]);
    expect(items[1]?.values?.remaining).toBe('10 min');
  });

  it('treats missing evidence as failing and no soak as not required', () => {
    const items = gateChecklist(
      ring({ freshObserved: 0, successRatePercent: null, soakMinutes: 0 }),
      planRing,
      {},
      now,
    );
    expect(state(items, 'threshold')).toBe('fail');
    expect(state(items, 'soak')).toBe('na');
    expect(state(items, 'window')).toBe('na');
  });

  it('prefers the countdown that ran down since the response', () => {
    const items = gateChecklist(ring({ soakRemainingSeconds: 600 }), planRing, {}, now, 0);
    expect(state(items, 'soak')).toBe('pass');
  });
});

describe('reason codes', () => {
  it('maps codes to categories and flags those that need a look', () => {
    expect(reasonCategory('version_revoked')).toBe('gate');
    expect(reasonCategory('hash_mismatch')).toBe('gate');
    expect(reasonCategory('failure_threshold')).toBe('quality');
    expect(reasonCategory('assignment_failed')).toBe('writer');
    expect(reasonCategory('change_window_closed')).toBe('schedule');
    expect(reasonCategory('manual_pause')).toBe('manual');
    expect(reasonCategory('artifact_changed')).toBe('gate');
    expect(reasonCategory('no_evidence')).toBe('quality');
    expect(reasonCategory('brand_new_code')).toBe('other');
    expect(reasonCategory(null)).toBe('other');
    expect(reasonNeedsAttention('product_blocked')).toBe(true);
    expect(reasonNeedsAttention('manual_halt')).toBe(false);
  });
});

describe('run actions', () => {
  const all = () => true;
  it('offers operations by status', () => {
    expect(
      runActions({ status: 'scheduled', highImpact: false }, all, false).map((a) => a.id),
    ).toEqual(['start', 'cancel']);
    expect(
      runActions({ status: 'running', highImpact: false }, all, false).map((a) => a.id),
    ).toEqual(['pause', 'halt', 'cancel']);
    expect(
      runActions({ status: 'paused', highImpact: false }, all, false).map((a) => a.id),
    ).toEqual(['resume', 'halt', 'cancel']);
    expect(runActions({ status: 'completed', highImpact: false }, all, false)).toEqual([]);
  });

  it('explains disabled operations', () => {
    expect(runActions({ status: 'scheduled', highImpact: false }, () => false, false)[0]).toEqual({
      id: 'start',
      disabledReason: 'deployments.run.reason.needsExecute',
    });
    expect(
      runActions({ status: 'scheduled', highImpact: false }, all, true)[0]?.disabledReason,
    ).toBe('deployments.run.reason.writeDisabled');
    const noHighImpact = (permission: string) => permission !== 'deployments.high_impact';
    expect(
      runActions({ status: 'scheduled', highImpact: true }, noHighImpact, false)[0]?.disabledReason,
    ).toBe('deployments.reason.needsHighImpact');
    // Pausing and halting never wait for the write capability.
    const halt = runActions({ status: 'running', highImpact: false }, all, true).find(
      (a) => a.id === 'halt',
    );
    expect(halt?.disabledReason).toBeUndefined();
  });

  it('offers ring operations for the ring state', () => {
    expect(ringActions(ring(), 'running', all, false).map((a) => a.id)).toEqual([
      'halt',
      'promote',
    ]);
    expect(
      ringActions(ring({ nextGate: 'approval' }), 'running', all, false).map((a) => [
        a.id,
        a.disabledReason,
      ]),
    ).toEqual([
      ['halt', undefined],
      ['requestApproval', undefined],
      ['promote', 'deployments.run.reason.approvalFirst'],
    ]);
    expect(ringActions(ring({ status: 'halted' }), 'paused', all, false).map((a) => a.id)).toEqual([
      'resume',
    ]);
    expect(ringActions(ring(), 'completed', all, false)).toEqual([]);
    const failed = { status: 'halted', statusReason: 'assignment_failed' } as const;
    expect(ringActions(ring({ ...failed, retryCount: 1 }), 'paused', all, false)).toEqual([
      { id: 'retry' },
    ]);
    expect(
      ringActions(ring({ ...failed, retryCount: 3 }), 'paused', all, false)[0]?.disabledReason,
    ).toBe('deployments.run.reason.retryLimit');
  });
});
