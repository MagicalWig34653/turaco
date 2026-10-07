import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { FilterBar } from '../../platform/ui/FilterBar';
import { TableDate } from '../../platform/ui/TableDate';
import { Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { executionApi } from './api';
import { CancelDialog } from './CancelDialog';
import { Section } from './components';
import { codeKey, sortRings } from './helpers';
import {
  attemptTone,
  focusRing,
  maxRingRetries,
  formatCountdown,
  gateChecklist,
  pollIntervalMs,
  progressSegments,
  rateLabel,
  reasonCategory,
  reasonNeedsAttention,
  remainingSoak,
  ringActions,
  ringTone,
  runActions,
  settledPercent,
  shouldPoll,
  targetStateOrder,
  targetTone,
  targetTotal,
  type RingAction,
  type RunAction,
} from './runHelpers';
import {
  haltReasons,
  type DeploymentAttempt,
  type DeploymentDetail,
  type DeploymentTarget,
  type RingProgress,
} from './types';

/** Calls `tick` every interval while enabled and the tab is visible; refreshes when it becomes visible again. */
function usePolling(tick: () => void, enabled: boolean, intervalMs = pollIntervalMs) {
  const ref = useRef(tick);
  ref.current = tick;
  useEffect(() => {
    if (!enabled) return undefined;
    let timer: number | undefined;
    const stop = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
    };
    const start = () => {
      stop();
      timer = window.setInterval(() => ref.current(), intervalMs);
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') stop();
      else {
        ref.current();
        start();
      }
    };
    if (document.visibilityState !== 'hidden') start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [enabled, intervalMs]);
}

type RunDialogState =
  { kind: 'start' | 'halt' | 'cancel' } | { kind: 'ringHalt' | 'ringApproval'; ring: RingProgress };

/** The run workspace of a Deployment from `scheduled` on: live progress, ring gates, operations, targets, attempts. */
export function RunView({ plan, onChanged }: { plan: DeploymentDetail; onChanged: () => void }) {
  const { t } = useI18n();
  const { can } = useSession();
  const progress = useAsync((signal) => executionApi.progress(plan.id, signal), [plan.id]);
  const data = progress.data;
  const status = data?.deployment.status ?? plan.status;
  usePolling(progress.reload, shouldPoll(status));
  const [fetchedAt, setFetchedAt] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    setFetchedAt(Date.now());
    setNow(Date.now());
  }, [data]);
  // A newer Deployment version (the engine moved it) refreshes the plan detail too.
  const liveVersion = data?.deployment.version;
  useEffect(() => {
    if (liveVersion !== undefined && liveVersion !== plan.version) onChanged();
  }, [liveVersion, plan.version, onChanged]);
  const ticking = (data?.rings ?? []).some((ring) => ring.soakRemainingSeconds > 0);
  useEffect(() => {
    if (!ticking) return undefined;
    const timer = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(timer);
  }, [ticking]);

  const [dialog, setDialog] = useState<RunDialogState>();
  const [writeDisabled, setWriteDisabled] = useState(false);
  const [notice, setNotice] = useState('');
  const [opError, setOpError] = useState<ApiError>();
  const [busyOp, setBusyOp] = useState<string>();

  const version = data?.deployment.version ?? plan.version;
  const refresh = () => {
    progress.reload();
    onChanged();
  };
  const fail = (error: ApiError) => {
    if (error.code === 'endpoints.deploy_write_disabled') setWriteDisabled(true);
    if (error.code === 'endpoints.version_conflict') refresh();
    setOpError(error);
  };
  const perform = async (id: string, message: MessageKey, op: () => Promise<unknown>) => {
    setBusyOp(id);
    setOpError(undefined);
    setNotice('');
    try {
      await op();
      setNotice(t(message));
      refresh();
    } catch (cause) {
      fail(asApiError(cause));
    } finally {
      setBusyOp(undefined);
    }
  };

  if (progress.error && !data)
    return <ApiErrorAlert error={progress.error} onRetry={progress.reload} />;
  if (!data) return <Skeleton lines={6} />;

  const rings = [...data.rings].sort((a, b) => a.position - b.position);
  const planRings = new Map(sortRings(plan.rings).map((ring) => [ring.id, ring]));
  const actions = runActions({ status, highImpact: plan.highImpact }, can, writeDisabled);
  const reasonCode = data.deployment.statusReason;
  const runDone = ['completed', 'completed_with_errors', 'failed', 'cancelled'].includes(status);

  const onAction = (action: RunAction) => {
    if (action.id === 'pause')
      void perform('pause', 'deployments.run.notice.paused', () =>
        executionApi.pause(plan.id, version),
      );
    else if (action.id === 'resume')
      void perform('resume', 'deployments.run.notice.resumed', () =>
        executionApi.resume(plan.id, version),
      );
    else setDialog({ kind: action.id });
  };
  const onRingAction = (action: RingAction, ring: RingProgress) => {
    if (action.id === 'halt') setDialog({ kind: 'ringHalt', ring });
    else if (action.id === 'requestApproval') setDialog({ kind: 'ringApproval', ring });
    else if (action.id === 'retry')
      void perform(`retry-${ring.ringId}`, 'deployments.run.notice.ringRetried', () =>
        executionApi.resumeRing(plan.id, ring.ringId, version, true),
      );
    else if (action.id === 'resume')
      void perform(`resume-${ring.ringId}`, 'deployments.run.notice.ringResumed', () =>
        executionApi.resumeRing(plan.id, ring.ringId, version),
      );
    else
      void perform(`promote-${ring.ringId}`, 'deployments.run.notice.promoted', () =>
        executionApi.promoteRing(plan.id, ring.ringId, version),
      );
  };

  return (
    <section className="deployments-run" aria-label={t('deployments.run.title')}>
      <div className="deployments-card-heading">
        <h2>{t('deployments.run.title')}</h2>
        <span className="deployments-muted" aria-live="polite">
          {shouldPoll(status)
            ? t('deployments.run.live', { seconds: pollIntervalMs / 1000 })
            : t('deployments.run.notLive')}
        </span>
        <Button onClick={progress.reload} busy={progress.loading && !!data}>
          {t('deployments.run.refresh')}
        </Button>
      </div>
      {progress.error ? <ApiErrorAlert error={progress.error} onRetry={progress.reload} /> : null}
      <RunBanner status={status} reason={reasonCode} />
      {data.clearPending ? <Alert kind="warning">{t('deployments.run.clearPending')}</Alert> : null}
      {data.resolvingStuck ? (
        <Alert kind="warning">{t('deployments.run.resolvingStuck')}</Alert>
      ) : null}
      {rings.some((ring) => ring.clearFailed) ? (
        <Alert kind="warning">
          {t('deployments.run.clearFailed')}{' '}
          <Link to="/endpoint-findings">{t('deployments.run.targets.conflictLink')}</Link>
        </Alert>
      ) : null}
      {notice ? <Alert kind="success">{notice}</Alert> : null}
      {opError ? <ApiErrorAlert error={opError} /> : null}
      {actions.length > 0 ? (
        <RunActionBar actions={actions} busyOp={busyOp} onAction={onAction} />
      ) : !runDone ? null : (
        <p className="deployments-muted">{t('deployments.run.finished')}</p>
      )}

      <ul className="deployments-run-rings">
        {rings.map((ring) => (
          <RingRunCard
            key={ring.ringId}
            ring={ring}
            plan={plan}
            planRing={planRings.get(ring.ringId)}
            status={status}
            now={now}
            fetchedAt={fetchedAt}
            writeDisabled={writeDisabled}
            busyOp={busyOp}
            onAction={onRingAction}
          />
        ))}
      </ul>
      {rings.length === 0 ? (
        <p className="deployments-muted">{t('deployments.run.noRings')}</p>
      ) : null}

      <TargetsPanel plan={plan} rings={rings} />
      <AttemptsPanel plan={plan} rings={rings} stamp={String(data.deployment.version)} />

      {dialog?.kind === 'start' ? (
        <StartDialog
          plan={plan}
          onClose={() => setDialog(undefined)}
          onConfirm={async () => {
            await executionApi.start(plan.id, version);
            setDialog(undefined);
            setNotice(t('deployments.run.notice.started'));
            refresh();
          }}
          onFailed={fail}
        />
      ) : null}
      {dialog?.kind === 'halt' || dialog?.kind === 'ringHalt' ? (
        <HaltDialog
          ring={dialog.kind === 'ringHalt' ? dialog.ring : undefined}
          onClose={() => setDialog(undefined)}
          onConfirm={async (reason) => {
            if (dialog.kind === 'ringHalt')
              await executionApi.haltRing(plan.id, dialog.ring.ringId, reason, version);
            else await executionApi.halt(plan.id, reason, version);
            setDialog(undefined);
            setNotice(
              t(
                dialog.kind === 'ringHalt'
                  ? 'deployments.run.notice.ringHalted'
                  : 'deployments.run.notice.halted',
              ),
            );
            refresh();
          }}
          onFailed={fail}
        />
      ) : null}
      {dialog?.kind === 'ringApproval' ? (
        <RingApprovalDialog
          ring={dialog.ring}
          onClose={() => setDialog(undefined)}
          onConfirm={async (approver) => {
            await executionApi.requestApproval(plan.id, dialog.ring.ringId, approver, version);
            setDialog(undefined);
            setNotice(t('deployments.run.notice.approvalRequested'));
            refresh();
          }}
          onFailed={fail}
        />
      ) : null}
      {dialog?.kind === 'cancel' ? (
        <CancelDialog
          plan={{ ...plan, version, status }}
          onClose={() => setDialog(undefined)}
          onDone={() => {
            setDialog(undefined);
            setNotice(t('deployments.notice.cancelled'));
            refresh();
          }}
        />
      ) : null}
    </section>
  );
}

// ---- Banner and actions ----

function RunBanner({ status, reason }: { status: string; reason: string | null }) {
  const { t } = useI18n();
  if (!['paused', 'failed', 'completed_with_errors'].includes(status)) return null;
  const attention = reasonNeedsAttention(reason) || status === 'failed';
  return (
    <Alert kind={status === 'failed' ? 'error' : attention ? 'warning' : 'info'}>
      <strong>{t(codeKey('deployments.status', status, 'deployments.unknownValue'))}</strong>
      {reason ? (
        <>
          {': '}
          {t(codeKey('deployments.run.reason.code', reason, 'deployments.run.reason.code.other'), {
            code: reason,
          })}{' '}
          {t(`deployments.run.reason.category.${reasonCategory(reason)}`)}
        </>
      ) : null}
    </Alert>
  );
}

function RunActionBar({
  actions,
  busyOp,
  onAction,
}: {
  actions: RunAction[];
  busyOp: string | undefined;
  onAction: (action: RunAction) => void;
}) {
  const { t } = useI18n();
  return (
    <div className="deployments-run-actions">
      <div className="deployments-action-bar">
        {actions.map((action) => (
          <Button
            key={action.id}
            variant={
              action.id === 'halt' ? 'danger' : action.id === 'cancel' ? 'secondary' : 'primary'
            }
            className={action.id === 'halt' ? 'deployments-halt' : undefined}
            disabled={action.disabledReason !== undefined}
            busy={busyOp === action.id}
            aria-describedby={action.disabledReason ? `run-action-${action.id}` : undefined}
            onClick={() => onAction(action)}
          >
            {t(`deployments.run.action.${action.id}`)}
          </Button>
        ))}
      </div>
      {actions
        .filter((action) => action.disabledReason)
        .map((action) => (
          <p key={action.id} id={`run-action-${action.id}`} className="deployments-disabled-note">
            {t(`deployments.run.action.${action.id}`)}: {t(action.disabledReason as MessageKey)}
          </p>
        ))}
    </div>
  );
}

// ---- Ring cards ----

function RingRunCard({
  ring,
  plan,
  planRing,
  status,
  now,
  fetchedAt,
  writeDisabled,
  busyOp,
  onAction,
}: {
  ring: RingProgress;
  plan: DeploymentDetail;
  planRing: DeploymentDetail['rings'][number] | undefined;
  status: string;
  now: number;
  fetchedAt: number;
  writeDisabled: boolean;
  busyOp: string | undefined;
  onAction: (action: RingAction, ring: RingProgress) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const total = targetTotal(ring.counts);
  const segments = progressSegments(ring.counts);
  const soak = remainingSoak(ring.soakRemainingSeconds, fetchedAt, now);
  const gates = gateChecklist(ring, planRing, plan.changes, new Date(now), soak);
  const actions = ringActions(ring, status, can, writeDisabled);
  const rate = rateLabel(ring.successRatePercent);
  const showGates = ring.status === 'active' || ring.status === 'awaiting_promotion';
  const summary = segments
    .map(
      (s) =>
        `${t(codeKey('deployments.run.target', s.state, 'deployments.unknownValue'))}: ${s.count}`,
    )
    .join(', ');
  return (
    <li className="deployments-run-ring">
      <div className="deployments-card-heading">
        <h3>
          <span className="deployments-mono">{ring.position}</span> {ring.name}
        </h3>
        <StatusBadge tone={ringTone(ring.status)} live={ring.status === 'active'}>
          {t(codeKey('deployments.run.ring', ring.status, 'deployments.unknownValue'))}
        </StatusBadge>
      </div>
      {ring.statusReason ? (
        <p className="deployments-muted">
          {t(
            codeKey(
              'deployments.run.reason.code',
              ring.statusReason,
              'deployments.run.reason.code.other',
            ),
            {
              code: ring.statusReason,
            },
          )}
        </p>
      ) : null}
      {ring.status === 'halted' && ring.statusReason === 'assignment_failed' ? (
        <p className="deployments-muted">
          {t('deployments.run.retries', { n: ring.retryCount ?? 0, max: maxRingRetries })}
        </p>
      ) : null}
      <div
        className="deployments-progress"
        role="img"
        aria-label={`${t('deployments.run.progress', { percent: settledPercent(ring.counts), total })}. ${summary}`}
      >
        {segments.map((segment) => (
          <span
            key={segment.state}
            className={`deployments-progress-seg deployments-progress-${targetTone(segment.state)}`}
            style={{ width: `${segment.percent}%` }}
          />
        ))}
      </div>
      <ul className="deployments-progress-legend">
        {targetStateOrder
          .filter((state) => (ring.counts[state] ?? 0) > 0)
          .map((state) => (
            <li key={state}>
              <StatusBadge tone={targetTone(state)}>
                {t(codeKey('deployments.run.target', state, 'deployments.unknownValue'))}
              </StatusBadge>{' '}
              <span className="deployments-count">{ring.counts[state]}</span>
            </li>
          ))}
        {total === 0 ? (
          <li className="deployments-muted">{t('deployments.run.noTargets')}</li>
        ) : null}
      </ul>
      <p className="deployments-muted">
        {rate
          ? t('deployments.run.successRate', {
              rate,
              fresh: ring.freshSuccessful,
              observed: ring.freshObserved,
            })
          : t('deployments.run.noEvidence')}
        {ring.soakMinutes > 0 && soak > 0 ? (
          <>
            {' · '}
            {t('deployments.run.soakLeft', { remaining: formatCountdown(soak) })}
          </>
        ) : null}
      </p>
      {showGates ? (
        <div>
          <h4>
            {t('deployments.run.gates')}
            {': '}
            <span className="deployments-muted">
              {t(
                codeKey(
                  'deployments.run.nextGate',
                  ring.nextGate,
                  'deployments.run.nextGate.other',
                ),
              )}
            </span>
          </h4>
          <ul className="deployments-gates">
            {gates.map((gate) => (
              <li key={gate.id} className={`deployments-gate deployments-gate-${gate.state}`}>
                <span aria-hidden="true">
                  {gate.state === 'pass' ? '✓' : gate.state === 'fail' ? '✗' : '–'}
                </span>
                <span className="visually-hidden">
                  {t(`deployments.run.gate.state.${gate.state}`)}:{' '}
                </span>
                <strong>{t(`deployments.run.gate.${gate.id}`)}</strong>
                {' · '}
                {t(gate.reason, gate.values)}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
      {actions.length > 0 ? (
        <div className="deployments-action-bar">
          {actions.map((action) => (
            <Button
              key={action.id}
              variant={action.id === 'halt' ? 'danger' : 'secondary'}
              disabled={action.disabledReason !== undefined}
              busy={busyOp === `${action.id}-${ring.ringId}`}
              title={action.disabledReason ? t(action.disabledReason) : undefined}
              onClick={() => onAction(action, ring)}
            >
              {t(`deployments.run.ringAction.${action.id}`)}
            </Button>
          ))}
        </div>
      ) : null}
    </li>
  );
}

// ---- Dialogs ----

function useConfirm(onFailed: (error: ApiError) => void) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
    } catch (cause) {
      const apiError = asApiError(cause);
      setError(apiError);
      onFailed(apiError);
      setBusy(false);
    }
  };
  return { busy, error, run };
}

function StartDialog({
  plan,
  onClose,
  onConfirm,
  onFailed,
}: {
  plan: DeploymentDetail;
  onClose: () => void;
  onConfirm: () => Promise<void>;
  onFailed: (error: ApiError) => void;
}) {
  const { t } = useI18n();
  const { busy, error, run } = useConfirm(onFailed);
  const targets = plan.validation.totalTargets;
  return (
    <Dialog title={t('deployments.run.start.title', { name: plan.name })} onClose={onClose}>
      <div className="dialog-body">
        <p>{t('deployments.run.start.text')}</p>
        <dl className="deployments-facts">
          <dt>{t('deployments.list.software')}</dt>
          <dd>
            {plan.productName} <span className="deployments-mono">{plan.productVersion}</span>
          </dd>
          <dt>{t('deployments.field.intent')}</dt>
          <dd>{t(`deployments.intent.${plan.intent}` as MessageKey)}</dd>
          <dt>{t('deployments.run.start.rings')}</dt>
          <dd>{plan.rings.length}</dd>
          <dt>{t('deployments.run.start.targets')}</dt>
          <dd>
            {typeof targets === 'number'
              ? plan.validation.incomplete
                ? t('deployments.run.start.targetsAtLeast', { count: targets })
                : targets
              : t('deployments.run.start.targetsUnknown')}
          </dd>
        </dl>
        {plan.highImpact ? (
          <Alert kind="warning">{t('deployments.run.start.highImpact')}</Alert>
        ) : null}
        <p className="deployments-muted">{t('deployments.run.start.note')}</p>
      </div>
      {error ? <ApiErrorAlert error={error} /> : null}
      <div className="dialog-actions">
        <Button onClick={onClose} autoFocus>
          {t('action.cancel')}
        </Button>
        <Button variant="primary" busy={busy} onClick={() => void run(onConfirm)}>
          {t('deployments.run.action.start')}
        </Button>
      </div>
    </Dialog>
  );
}

function HaltDialog({
  ring,
  onClose,
  onConfirm,
  onFailed,
}: {
  ring: RingProgress | undefined;
  onClose: () => void;
  onConfirm: (reason: string) => Promise<void>;
  onFailed: (error: ApiError) => void;
}) {
  const { t } = useI18n();
  const { busy, error, run } = useConfirm(onFailed);
  const [reason, setReason] = useState('');
  const submit = (event: FormEvent) => {
    event.preventDefault();
    void run(() => onConfirm(reason));
  };
  return (
    <Dialog
      title={
        ring
          ? t('deployments.run.haltRing.title', { name: ring.name })
          : t('deployments.run.halt.title')
      }
      onClose={onClose}
    >
      <form className="form" onSubmit={submit}>
        <div className="dialog-body">
          <p>{ring ? t('deployments.run.haltRing.text') : t('deployments.run.halt.text')}</p>
        </div>
        <Select
          label={t('deployments.run.halt.reason')}
          value={reason}
          required
          autoFocus
          onChange={(event) => setReason(event.target.value)}
          options={[
            { value: '', label: t('deployments.cancel.choose') },
            ...haltReasons.map((value) => ({
              value,
              label: t(`deployments.run.haltReason.${value}`),
            })),
          ]}
        />
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="danger" busy={busy} disabled={!reason}>
            {ring ? t('deployments.run.ringAction.halt') : t('deployments.run.action.halt')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function RingApprovalDialog({
  ring,
  onClose,
  onConfirm,
  onFailed,
}: {
  ring: RingProgress;
  onClose: () => void;
  onConfirm: (approver: { type: 'user' | 'team'; id: string }) => Promise<void>;
  onFailed: (error: ApiError) => void;
}) {
  const { t } = useI18n();
  const { busy, error, run } = useConfirm(onFailed);
  const [type, setType] = useState<'user' | 'team'>('user');
  const [approver, setApprover] = useState<Assignee | null>(null);
  return (
    <Dialog title={t('deployments.run.approval.title', { name: ring.name })} onClose={onClose} wide>
      <form
        className="form"
        onSubmit={(event) => {
          event.preventDefault();
          if (approver) void run(() => onConfirm({ type, id: approver.id }));
        }}
      >
        <div className="dialog-body">
          <p>{t('deployments.run.approval.text')}</p>
        </div>
        <Select
          label={t('deployments.submit.approverType')}
          value={type}
          onChange={(event) => {
            setType(event.target.value as 'user' | 'team');
            setApprover(null);
          }}
          options={[
            { value: 'user', label: t('deployments.submit.user') },
            { value: 'team', label: t('deployments.submit.team') },
          ]}
        />
        <AssigneePicker type={type} value={approver} onChange={setApprover} />
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!approver}>
            {t('deployments.run.ringAction.requestApproval')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ---- Targets and attempts ----

function TargetsPanel({ plan, rings }: { plan: DeploymentDetail; rings: RingProgress[] }) {
  const { t } = useI18n();
  const [selected, setSelected] = useState('');
  const [state, setState] = useState('');
  const ring = rings.find((r) => r.ringId === selected) ?? focusRing(rings);
  const signature = JSON.stringify(ring?.counts ?? {});
  const list = usePagedList(
    (cursor, signal) => executionApi.targets(plan.id, ring?.ringId ?? '', state, cursor, signal),
    [plan.id, ring?.ringId, state, signature],
  );
  const hasNames = list.items.some((target) => target.deviceName);
  const columns: Column<DeploymentTarget>[] = [
    ...(hasNames
      ? [
          {
            key: 'device',
            header: t('deployments.run.targets.device'),
            render: (target: DeploymentTarget) => (
              <Link to={`/devices/${encodeURIComponent(target.deviceId)}`}>
                {target.deviceName ?? target.deviceId}
              </Link>
            ),
          },
        ]
      : []),
    {
      key: 'state',
      header: t('deployments.run.targets.state'),
      render: (target) => (
        <>
          <StatusBadge tone={targetTone(target.state)}>
            {t(codeKey('deployments.run.target', target.state, 'deployments.unknownValue'))}
          </StatusBadge>
          {target.stateReason ? (
            <small className="deployments-muted">
              {' '}
              {t(
                codeKey(
                  'deployments.run.reason.code',
                  target.stateReason,
                  'deployments.run.reason.code.other',
                ),
                {
                  code: target.stateReason,
                },
              )}
            </small>
          ) : null}
        </>
      ),
    },
    {
      key: 'observed',
      header: t('deployments.run.targets.observed'),
      render: (target) =>
        target.evidenceObservedAt ? (
          <TableDate value={target.evidenceObservedAt} />
        ) : (
          <span className="deployments-muted">{t('deployments.run.targets.noObservation')}</span>
        ),
    },
    {
      key: 'decided',
      header: t('deployments.run.targets.decided'),
      render: (target) => <TableDate value={target.decidedAt} />,
    },
  ];
  return (
    <Section title={t('deployments.run.targets.title')}>
      <FilterBar aria-label={t('deployments.run.targets.filters')}>
        <Select
          label={t('deployments.run.targets.ring')}
          value={ring?.ringId ?? ''}
          onChange={(event) => setSelected(event.target.value)}
          options={rings.map((r) => ({ value: r.ringId, label: `${r.position}. ${r.name}` }))}
        />
        <Select
          label={t('deployments.run.targets.state')}
          value={state}
          onChange={(event) => setState(event.target.value)}
          options={[
            { value: '', label: t('deployments.run.targets.allStates') },
            ...targetStateOrder.map((value) => ({
              value,
              label: t(codeKey('deployments.run.target', value, 'deployments.unknownValue')),
            })),
          ]}
        />
      </FilterBar>
      <DataTable
        caption={t('deployments.run.targets.title')}
        columns={columns}
        rows={list.items}
        rowKey={(target) => target.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('deployments.run.targets.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      <p className="deployments-muted">
        {t('deployments.run.targets.conflictNote')}{' '}
        <Link to="/endpoint-findings">{t('deployments.run.targets.conflictLink')}</Link>
      </p>
    </Section>
  );
}

function AttemptsPanel({
  plan,
  rings,
  stamp,
}: {
  plan: DeploymentDetail;
  rings: RingProgress[];
  stamp: string;
}) {
  const { t, locale } = useI18n();
  const list = usePagedList(
    (cursor, signal) => executionApi.attempts(plan.id, cursor, signal),
    [plan.id, stamp],
  );
  const ringName = (attempt: DeploymentAttempt) =>
    rings.find((ring) => ring.ringRunId === attempt.ringRunId)?.name ?? '';
  return (
    <Section title={t('deployments.run.attempts.title')}>
      {list.loading ? (
        <Skeleton lines={3} />
      ) : list.error ? (
        <ApiErrorAlert error={list.error} onRetry={list.reload} />
      ) : list.items.length === 0 ? (
        <p className="deployments-muted">{t('deployments.run.attempts.empty')}</p>
      ) : (
        <ol className="deployments-history">
          {list.items.map((attempt) => (
            <li key={attempt.id}>
              <strong>
                {t(codeKey('deployments.run.attempt', attempt.kind, 'deployments.unknownValue'))}
              </strong>{' '}
              <StatusBadge tone={attemptTone(attempt.outcomeCode)}>
                {t(
                  codeKey(
                    'deployments.run.outcome',
                    attempt.outcomeCode,
                    'deployments.unknownValue',
                  ),
                )}
              </StatusBadge>{' '}
              <span className="deployments-muted">
                {ringName(attempt) ? `${ringName(attempt)} · ` : ''}
                {t('deployments.run.attempts.number', { n: attempt.attempt })}
                {' · '}
                <time dateTime={attempt.requestedAt}>
                  {formatDateTime(locale, attempt.requestedAt)}
                </time>
              </span>
            </li>
          ))}
        </ol>
      )}
      {list.hasMore ? (
        <Button onClick={list.loadMore} busy={list.loadingMore}>
          {t('action.loadMore')}
        </Button>
      ) : null}
    </Section>
  );
}
