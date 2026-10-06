import type { MessageKey } from '../../platform/i18n/i18n';
import { windowState, type Tone } from './helpers';
import type {
  ChangeWindow,
  Deployment,
  DeploymentRing,
  RingProgress,
  targetStates as targetStateList,
} from './types';

/** Progress is polled while the Deployment can still change by itself. */
export const pollIntervalMs = 15_000;
const livePollStatuses = ['resolving_targets', 'ready', 'running'];
export const shouldPoll = (status: string) => livePollStatuses.includes(status);

export type TargetState = (typeof targetStateList)[number];

/** Display order of the target states in bars and filters. */
export const targetStateOrder: readonly string[] = [
  'successful',
  'already_satisfied',
  'awaiting_observation',
  'assignment_requested',
  'pending',
  'failed',
  'expired',
  'not_applicable',
  'cancelled',
];

export function targetTone(state: string): Tone {
  switch (state) {
    case 'successful':
    case 'already_satisfied':
      return 'success';
    case 'failed':
      return 'danger';
    case 'expired':
      return 'warning';
    case 'assignment_requested':
    case 'awaiting_observation':
      return 'info';
    case 'pending':
    case 'not_applicable':
    case 'cancelled':
      return 'neutral';
    default:
      return 'unknown';
  }
}

export function ringTone(status: string): Tone {
  switch (status) {
    case 'active':
      return 'info';
    case 'awaiting_promotion':
      return 'warning';
    case 'promoted':
      return 'success';
    case 'halted':
      return 'danger';
    case 'pending':
      return 'neutral';
    default:
      return 'unknown';
  }
}

export const attemptTone = (outcome: string): Tone =>
  outcome === 'accepted' ? 'success' : outcome === 'transient_error' ? 'warning' : 'danger';

export type BarSegment = { state: string; count: number; percent: number };

export const targetTotal = (counts: Record<string, number> | undefined) =>
  Object.values(counts ?? {}).reduce((sum, n) => sum + n, 0);

/**
 * Segments of the ring progress bar in display order; zero counts are left out. Percentages are of
 * all targets of the ring and are rounded so they never add up to more than 100.
 */
export function progressSegments(counts: Record<string, number> | undefined): BarSegment[] {
  const total = targetTotal(counts);
  if (total === 0) return [];
  const known = targetStateOrder.filter((state) => (counts?.[state] ?? 0) > 0);
  const unknown = Object.keys(counts ?? {})
    .filter((state) => !targetStateOrder.includes(state) && (counts?.[state] ?? 0) > 0)
    .sort();
  let used = 0;
  return [...known, ...unknown].map((state, index, all) => {
    const count = counts?.[state] ?? 0;
    const percent =
      index === all.length - 1 ? Math.max(0, 100 - used) : Math.floor((count / total) * 100);
    used += percent;
    return { state, count, percent };
  });
}

/** Share of the ring's targets that reached a final state of any kind, as a whole percent. */
export function settledPercent(counts: Record<string, number> | undefined): number {
  const total = targetTotal(counts);
  if (total === 0) return 0;
  const open = ['pending', 'assignment_requested', 'awaiting_observation'];
  const settled = Object.entries(counts ?? {})
    .filter(([state]) => !open.includes(state))
    .reduce((sum, [, n]) => sum + n, 0);
  return Math.floor((settled / total) * 100);
}

/** The success rate on fresh evidence; null while nothing has been observed. */
export function rateLabel(rate: number | null | undefined): string | null {
  if (rate === null || rate === undefined || Number.isNaN(rate)) return null;
  return `${Math.round(rate * 10) / 10}%`;
}

export type Countdown = { days: number; hours: number; minutes: number };

/** Remaining soak time for display; rounds up so "0 min" is only shown for an elapsed soak. */
export function countdownParts(seconds: number): Countdown {
  const total = Math.max(0, Math.ceil(seconds / 60));
  return {
    days: Math.floor(total / 1440),
    hours: Math.floor((total % 1440) / 60),
    minutes: total % 60,
  };
}

export function formatCountdown(seconds: number): string {
  const { days, hours, minutes } = countdownParts(seconds);
  const parts = [
    days > 0 ? `${days} d` : '',
    hours > 0 ? `${hours} h` : '',
    minutes > 0 || (days === 0 && hours === 0) ? `${minutes} min` : '',
  ];
  return parts.filter(Boolean).join(' ');
}

/** Soak left now, given what the last response said and when it arrived. */
export const remainingSoak = (seconds: number, fetchedAt: number, now: number) =>
  Math.max(0, seconds - Math.floor((now - fetchedAt) / 1000));

export type GateId = 'threshold' | 'soak' | 'approval' | 'window';
export type GateItem = {
  id: GateId;
  /** pass: satisfied; fail: blocking now; na: not required for this ring. */
  state: 'pass' | 'fail' | 'na';
  reason: MessageKey;
  /** Interpolation values of the reason, e.g. the countdown. */
  values?: Record<string, string | number>;
};

/**
 * The promotion gates of a ring as a checklist: success threshold on fresh evidence, soak, approval
 * and Change window. The API decides; this explains what it will check.
 */
export function gateChecklist(
  progress: RingProgress,
  ring: DeploymentRing | undefined,
  changes: Record<string, ChangeWindow>,
  now: Date,
  soakSeconds = progress.soakRemainingSeconds,
): GateItem[] {
  const items: GateItem[] = [];
  const rate = progress.successRatePercent;
  if (progress.freshObserved === 0 || rate === null) {
    items.push({
      id: 'threshold',
      state: 'fail',
      reason: 'deployments.run.gate.threshold.noEvidence',
    });
  } else if (rate >= progress.successThresholdPercent) {
    items.push({
      id: 'threshold',
      state: 'pass',
      reason: 'deployments.run.gate.threshold.met',
      values: { rate: rateLabel(rate) ?? '', threshold: progress.successThresholdPercent },
    });
  } else {
    items.push({
      id: 'threshold',
      state: 'fail',
      reason: 'deployments.run.gate.threshold.below',
      values: { rate: rateLabel(rate) ?? '', threshold: progress.successThresholdPercent },
    });
  }

  if (progress.soakMinutes === 0) {
    items.push({ id: 'soak', state: 'na', reason: 'deployments.run.gate.soak.none' });
  } else if (soakSeconds > 0) {
    items.push({
      id: 'soak',
      state: 'fail',
      reason: 'deployments.run.gate.soak.remaining',
      values: { remaining: formatCountdown(soakSeconds) },
    });
  } else if (progress.settledAt === null && progress.awaitingSince === null) {
    items.push({ id: 'soak', state: 'fail', reason: 'deployments.run.gate.soak.notStarted' });
  } else {
    items.push({ id: 'soak', state: 'pass', reason: 'deployments.run.gate.soak.elapsed' });
  }

  if (!progress.approvalRequired) {
    items.push({ id: 'approval', state: 'na', reason: 'deployments.run.gate.approval.none' });
  } else if (progress.promotionApprovalStatus === 'approved') {
    items.push({ id: 'approval', state: 'pass', reason: 'deployments.run.gate.approval.approved' });
  } else if (progress.promotionApprovalStatus === 'pending') {
    items.push({ id: 'approval', state: 'fail', reason: 'deployments.run.gate.approval.pending' });
  } else if (progress.promotionApprovalStatus === 'rejected') {
    items.push({ id: 'approval', state: 'fail', reason: 'deployments.run.gate.approval.rejected' });
  } else {
    items.push({ id: 'approval', state: 'fail', reason: 'deployments.run.gate.approval.missing' });
  }

  const change = ring?.changeId ? changes[ring.changeId] : undefined;
  if (!ring?.changeId) {
    items.push({ id: 'window', state: 'na', reason: 'deployments.run.gate.window.none' });
  } else {
    const state = windowState(change, now);
    items.push({
      id: 'window',
      state: state === 'open' ? 'pass' : 'fail',
      reason: `deployments.run.gate.window.${state}`,
    });
  }
  return items;
}

/** How a pause/halt reason is explained and how serious it is. */
export type ReasonCategory = 'manual' | 'quality' | 'gate' | 'writer' | 'schedule' | 'other';

const reasonCategories: Record<string, ReasonCategory> = {
  manual_pause: 'manual',
  manual_halt: 'manual',
  quality_issue: 'manual',
  security_risk: 'manual',
  ring_halted: 'manual',
  failure_threshold: 'quality',
  threshold_not_met: 'quality',
  hash_mismatch: 'gate',
  version_revoked: 'gate',
  product_blocked: 'gate',
  package_gate_closed: 'gate',
  package_not_published: 'gate',
  artifact_not_linked: 'gate',
  assignment_failed: 'writer',
  change_window_closed: 'schedule',
  window_closed: 'schedule',
};

export const reasonCategory = (code: string | null | undefined): ReasonCategory =>
  (code ? reasonCategories[code] : undefined) ?? 'other';

/** Gate and quality stops are not lifted by resuming alone; a person has to look at the cause. */
export const reasonNeedsAttention = (code: string | null | undefined) =>
  ['quality', 'gate', 'writer'].includes(reasonCategory(code));

export type RunActionId = 'start' | 'pause' | 'resume' | 'halt' | 'cancel';
export type RunAction = { id: RunActionId; disabledReason?: MessageKey };

const cancellable = ['scheduled', 'ready', 'running', 'paused'];

/**
 * Deployment-level operations of the run view with the reason when one is not possible. The API
 * still decides; `writeDisabled` is set once it answered endpoints.deploy_write_disabled.
 */
export function runActions(
  plan: Pick<Deployment, 'status' | 'highImpact'>,
  can: (permission: string) => boolean,
  writeDisabled: boolean,
): RunAction[] {
  const actions: RunAction[] = [];
  const guard = (writes: boolean): MessageKey | undefined =>
    !can('deployments.execute')
      ? 'deployments.run.reason.needsExecute'
      : plan.highImpact && !can('deployments.high_impact')
        ? 'deployments.reason.needsHighImpact'
        : writes && writeDisabled
          ? 'deployments.run.reason.writeDisabled'
          : undefined;
  const add = (id: RunActionId, reason: MessageKey | undefined) =>
    actions.push({ id, ...(reason ? { disabledReason: reason } : {}) });
  if (plan.status === 'scheduled' || plan.status === 'ready') add('start', guard(true));
  if (plan.status === 'running') add('pause', guard(false));
  if (plan.status === 'paused') add('resume', guard(true));
  if (['resolving_targets', 'ready', 'running', 'paused'].includes(plan.status))
    add('halt', can('deployments.execute') ? undefined : 'deployments.run.reason.needsExecute');
  if (cancellable.includes(plan.status))
    add(
      'cancel',
      can('deployments.execute') || can('deployments.manage')
        ? undefined
        : 'deployments.run.reason.needsExecute',
    );
  return actions;
}

export type RingActionId = 'halt' | 'resume' | 'promote' | 'requestApproval';
export type RingAction = { id: RingActionId; disabledReason?: MessageKey };

/** Ring-level operations for one ring of a running or paused Deployment. */
export function ringActions(
  ring: RingProgress,
  deploymentStatus: string,
  can: (permission: string) => boolean,
  writeDisabled: boolean,
): RingAction[] {
  if (!['running', 'paused'].includes(deploymentStatus)) return [];
  const needs = can('deployments.execute') ? undefined : 'deployments.run.reason.needsExecute';
  const actions: RingAction[] = [];
  const add = (id: RingActionId, reason: MessageKey | undefined) =>
    actions.push({ id, ...(reason ? { disabledReason: reason } : {}) });
  if (ring.status === 'active' || ring.status === 'awaiting_promotion') add('halt', needs);
  if (ring.status === 'halted')
    add('resume', needs ?? (writeDisabled ? 'deployments.run.reason.writeDisabled' : undefined));
  if (ring.status === 'awaiting_promotion') {
    if (ring.nextGate === 'approval') add('requestApproval', needs);
    add(
      'promote',
      needs ??
        (writeDisabled
          ? 'deployments.run.reason.writeDisabled'
          : deploymentStatus === 'paused'
            ? 'deployments.run.reason.pausedDeployment'
            : ring.nextGate === 'approval'
              ? 'deployments.run.reason.approvalFirst'
              : undefined),
    );
  }
  return actions;
}

/** The ring a person most likely wants to look at: the active or awaiting one, else the last. */
export function focusRing(rings: readonly RingProgress[]): RingProgress | undefined {
  return (
    rings.find((ring) => ring.status === 'active' || ring.status === 'awaiting_promotion') ??
    rings.find((ring) => ring.status === 'halted') ??
    rings[rings.length - 1]
  );
}
