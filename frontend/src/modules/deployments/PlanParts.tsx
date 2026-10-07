import { useId, useState, type DragEvent, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { ConfirmDialog, Dialog } from '../../platform/ui/Dialog';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { StatusBadge } from '../../platform/ui/Workspace';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { deploymentsApi, targetSetsApi } from './api';
import {
  ChangePicker,
  HighImpactBadge,
  IssueText,
  Section,
  VersionSelect,
  WindowPeriod,
  canViewChanges,
} from './components';
import {
  blockingIssues,
  codeKey,
  intentIsHighImpact,
  moveItem,
  ringFormValues,
  ringInput,
  ringIssues,
  ringLimits,
  ringOrderProblem,
  soakParts,
  sortRings,
  validateRingForm,
  warningIssues,
  type RingFormValues,
} from './helpers';
import {
  deploymentIntents,
  type DeploymentDetail,
  type DeploymentInput,
  type DeploymentIntent,
  type DeploymentRing,
  type PlanValidation,
} from './types';

const enc = encodeURIComponent;

// ---- Plan fields (wizard step 1 and the edit dialog) ----

export type PlanFieldValues = {
  name: string;
  versionId: string;
  versionLabel: string;
  intent: DeploymentIntent;
  supersede: boolean;
  createTasks: boolean;
  owner: Assignee | null;
};

export function planFieldValues(plan?: DeploymentDetail | undefined): PlanFieldValues {
  return {
    name: plan?.name ?? '',
    versionId: plan?.softwareVersionId ?? '',
    versionLabel: plan ? `${plan.productName} ${plan.productVersion}` : '',
    intent: plan?.intent ?? 'install',
    supersede: plan?.supersede ?? false,
    createTasks: plan?.createTasks ?? false,
    owner: null,
  };
}

export function planInput(values: PlanFieldValues): DeploymentInput {
  return {
    name: values.name.trim(),
    softwareVersionId: values.versionId,
    intent: values.intent,
    supersede: values.supersede,
    createTasks: values.createTasks,
    ...(values.owner ? { ownerUserId: values.owner.id } : {}),
  };
}

export function PlanFields({
  values,
  onChange,
  showErrors,
}: {
  values: PlanFieldValues;
  onChange: (values: PlanFieldValues) => void;
  showErrors: boolean;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const highImpact = intentIsHighImpact(values.intent, values.supersede);
  return (
    <>
      <TextField
        label={t('deployments.field.name')}
        value={values.name}
        required
        maxLength={150}
        error={showErrors && !values.name.trim() ? t('deployments.field.nameRequired') : undefined}
        onChange={(event) => onChange({ ...values, name: event.target.value })}
      />
      <VersionSelect
        value={values.versionId}
        currentLabel={values.versionLabel}
        error={showErrors && !values.versionId ? t('deployments.field.versionRequired') : undefined}
        onChange={(versionId, versionLabel) => onChange({ ...values, versionId, versionLabel })}
      />
      <Select
        label={t('deployments.field.intent')}
        value={values.intent}
        onChange={(event) =>
          onChange({ ...values, intent: event.target.value as DeploymentIntent })
        }
        options={deploymentIntents.map((intent) => ({
          value: intent,
          label: t(codeKey('deployments.intent', intent, 'deployments.unknownValue')),
        }))}
      />
      <Checkbox
        label={t('deployments.field.supersede')}
        description={t('deployments.field.supersedeHint')}
        checked={values.supersede}
        onChange={(event) => onChange({ ...values, supersede: event.target.checked })}
      />
      <Checkbox
        label={t('deployments.field.createTasks')}
        description={t('deployments.field.createTasksHint')}
        checked={values.createTasks}
        onChange={(event) => onChange({ ...values, createTasks: event.target.checked })}
      />
      {highImpact ? (
        <Alert kind="warning">
          {t('deployments.highImpactIntent')}
          {!can('deployments.high_impact') ? ` ${t('deployments.reason.needsHighImpact')}` : ''}
        </Alert>
      ) : null}
      <AssigneePicker
        type="user"
        value={values.owner}
        onChange={(owner) => onChange({ ...values, owner })}
        label={t('deployments.field.owner')}
        hint={t('deployments.field.ownerKeep')}
      />
    </>
  );
}

export function PlanFieldsDialog({
  plan,
  onClose,
  onDone,
}: {
  plan: DeploymentDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [values, setValues] = useState(() => planFieldValues(plan));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [tried, setTried] = useState(false);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTried(true);
    if (!values.name.trim() || !values.versionId) return;
    setBusy(true);
    setError(undefined);
    try {
      await deploymentsApi.update(plan.id, planInput(values), plan.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('deployments.plan.editDetails')} onClose={onClose} wide>
      <form className="form" onSubmit={(event) => void submit(event)} noValidate>
        <PlanFields values={values} onChange={setValues} showErrors={tried} />
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {t('action.save')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ---- Ring dialog ----

export function RingDialog({
  plan,
  ring,
  onClose,
  onDone,
}: {
  plan: DeploymentDetail;
  ring?: DeploymentRing | undefined;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const pilot = ring ? ring.position === 1 : plan.rings.length === 0;
  const [values, setValues] = useState<RingFormValues>(() => ({
    ...ringFormValues(ring),
    ...(ring ? {} : { name: pilot ? t('deployments.ring.pilotName') : '' }),
  }));
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const canReadSets =
    can('deployments.view') || can('deployments.manage') || can('deployments.execute');
  const sets = useAsync(
    (signal) =>
      canReadSets
        ? targetSetsApi.list(false, undefined, signal).then((page) => page.items)
        : Promise.resolve([]),
    [canReadSets],
  );
  const set = (patch: Partial<RingFormValues>) => setValues((prev) => ({ ...prev, ...patch }));
  const err = (key: keyof RingFormValues) => (tried && errors[key] ? t(errors[key]) : undefined);
  const chosenSet = sets.data?.find((item) => item.id === values.targetSetId);
  // Only the pilot may run without a window, and not on an all-devices or nested-root-group set.
  const windowFree = pilot && !chosenSet?.allDevices && !chosenSet?.highImpactReason;
  const effective = windowFree ? values : { ...values, noWindowRequired: false };
  const errors = validateRingForm(effective, pilot);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTried(true);
    if (Object.keys(errors).length > 0) return;
    setBusy(true);
    setError(undefined);
    try {
      if (ring)
        await deploymentsApi.updateRing(plan.id, ring.id, ringInput(effective), plan.version);
      else await deploymentsApi.addRing(plan.id, ringInput(effective), plan.version);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  const setOptions = [
    ...(values.targetSetId && !sets.data?.some((item) => item.id === values.targetSetId)
      ? [{ value: values.targetSetId, label: ring?.targetSetName ?? values.targetSetId }]
      : []),
    ...(sets.data ?? []).map((item) => ({
      value: item.id,
      label: `${item.name} (${item.reference})${item.allDevices ? ` · ${t('deployments.ts.allDevices')}` : ''}`,
    })),
  ];
  return (
    <Dialog
      title={
        ring ? t('deployments.ring.editTitle', { name: ring.name }) : t('deployments.ring.addTitle')
      }
      onClose={onClose}
      wide
    >
      <form
        className="form deployments-ring-form"
        onSubmit={(event) => void submit(event)}
        noValidate
      >
        {pilot ? <p className="deployments-muted">{t('deployments.ring.pilotHint')}</p> : null}
        <TextField
          label={t('deployments.ring.name')}
          value={values.name}
          maxLength={100}
          required
          error={err('name')}
          onChange={(event) => set({ name: event.target.value })}
        />
        {canReadSets ? (
          <Select
            label={t('deployments.ring.targetSet')}
            value={values.targetSetId}
            error={err('targetSetId')}
            disabled={sets.loading}
            onChange={(event) => set({ targetSetId: event.target.value })}
            options={[
              {
                value: '',
                label: sets.loading ? t('state.loading') : t('deployments.ring.targetSetChoose'),
              },
              ...setOptions,
            ]}
          />
        ) : (
          <TextField
            label={t('deployments.ring.targetSet')}
            hint={t('deployments.picker.idHint')}
            value={values.targetSetId}
            error={err('targetSetId')}
            onChange={(event) => set({ targetSetId: event.target.value.trim() })}
          />
        )}
        {sets.error ? <ApiErrorAlert error={sets.error} onRetry={sets.reload} /> : null}
        {chosenSet?.allDevices ? (
          <Alert kind="warning">{t('deployments.ring.allDevicesWarning')}</Alert>
        ) : null}
        <fieldset className="deployments-gate">
          <legend>{t('deployments.ring.gates')}</legend>
          <Checkbox
            label={t('deployments.ring.approvalRequired')}
            description={t('deployments.ring.approvalRequiredHint')}
            checked={values.approvalRequired}
            onChange={(event) => set({ approvalRequired: event.target.checked })}
          />
          <div className="deployments-form-grid">
            <TextField
              label={t('deployments.ring.successThreshold')}
              hint={t('deployments.ring.percentHint')}
              inputMode="numeric"
              value={values.successThresholdPercent}
              error={err('successThresholdPercent')}
              onChange={(event) => set({ successThresholdPercent: event.target.value })}
            />
            <TextField
              label={t('deployments.ring.minFresh')}
              hint={t('deployments.ring.minFreshHint')}
              inputMode="numeric"
              value={values.minFreshEvidencePercent}
              error={err('minFreshEvidencePercent')}
              onChange={(event) => set({ minFreshEvidencePercent: event.target.value })}
            />
            <TextField
              label={t('deployments.ring.soak')}
              inputMode="numeric"
              value={values.soakValue}
              error={err('soakValue')}
              onChange={(event) => set({ soakValue: event.target.value })}
            />
            <Select
              label={t('deployments.ring.soakUnit')}
              value={values.soakUnit}
              onChange={(event) =>
                set({ soakUnit: event.target.value as RingFormValues['soakUnit'] })
              }
              options={(['minutes', 'hours', 'days'] as const).map((unit) => ({
                value: unit,
                label: t(`deployments.unit.${unit}`),
              }))}
            />
            <TextField
              label={t('deployments.ring.maxTargets')}
              hint={t('deployments.ring.maxTargetsHint', { max: ringLimits.maxTargets })}
              inputMode="numeric"
              value={values.maxTargets}
              error={err('maxTargets')}
              onChange={(event) => set({ maxTargets: event.target.value })}
            />
          </div>
        </fieldset>
        <fieldset className="deployments-gate">
          <legend>{t('deployments.ring.window')}</legend>
          {windowFree ? (
            <Checkbox
              label={t('deployments.ring.noWindow')}
              description={t('deployments.ring.noWindowHint')}
              checked={values.noWindowRequired}
              onChange={(event) => set({ noWindowRequired: event.target.checked })}
            />
          ) : (
            <p className="deployments-muted">
              {pilot
                ? t('deployments.ring.windowHighImpact')
                : t('deployments.ring.windowRequired')}
            </p>
          )}
          {!effective.noWindowRequired ? (
            <ChangePicker
              value={values.changeId}
              known={values.changeId ? plan.changes[values.changeId] : undefined}
              error={err('changeId')}
              onChange={(changeId) => set({ changeId })}
            />
          ) : null}
          {tried && errors.noWindowRequired ? (
            <p className="field-error">{t(errors.noWindowRequired)}</p>
          ) : null}
        </fieldset>
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy}>
            {ring ? t('action.save') : t('deployments.ring.add')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

// ---- Ring timeline ----

function Soak({ minutes }: { minutes: number }) {
  const { t } = useI18n();
  const parts = soakParts(minutes);
  return <>{t(`deployments.soak.${parts.unit}`, { count: parts.value })}</>;
}

/**
 * Ordered vertical timeline of ring cards. In a draft plan the rings can be reordered with the
 * move buttons (keyboard) or by dragging; the API refuses orders that move a window-less pilot.
 */
export function RingTimeline({
  plan,
  validation,
  editable,
  onChanged,
}: {
  plan: DeploymentDetail;
  validation: PlanValidation;
  editable: boolean;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const rings = sortRings(plan.rings);
  const [editing, setEditing] = useState<DeploymentRing | 'new'>();
  const [removing, setRemoving] = useState<DeploymentRing>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [announce, setAnnounce] = useState('');
  const [dragId, setDragId] = useState<string>();
  const counts = new Map(validation.rings.map((entry) => [entry.ringId, entry]));
  const changeLinks = canViewChanges(can);

  const reorder = async (from: number, to: number) => {
    const order = moveItem(
      rings.map((ring) => ring.id),
      from,
      to,
    );
    const problem = ringOrderProblem(order, rings);
    if (problem) {
      setAnnounce(t(problem));
      return;
    }
    setBusy(true);
    setError(undefined);
    try {
      await deploymentsApi.reorderRings(plan.id, order, plan.version);
      setAnnounce(t('deployments.ring.moved', { name: rings[from]?.name ?? '', position: to + 1 }));
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    if (!removing) return;
    setBusy(true);
    setError(undefined);
    try {
      await deploymentsApi.removeRing(plan.id, removing.id, plan.version);
      setRemoving(undefined);
      onChanged();
    } catch (cause) {
      setError(asApiError(cause));
      setRemoving(undefined);
    } finally {
      setBusy(false);
    }
  };

  const onDrop = (event: DragEvent, to: number) => {
    event.preventDefault();
    const from = rings.findIndex((ring) => ring.id === dragId);
    setDragId(undefined);
    if (from >= 0 && from !== to) void reorder(from, to);
  };

  return (
    <Section
      title={t('deployments.ring.title', { count: rings.length, max: ringLimits.rings })}
      actions={
        editable && rings.length < ringLimits.rings ? (
          <Button onClick={() => setEditing('new')}>{t('deployments.ring.add')}</Button>
        ) : null
      }
    >
      {error ? <ApiErrorAlert error={error} /> : null}
      <p className="visually-hidden" aria-live="polite">
        {announce}
      </p>
      {announce && !busy ? <p className="deployments-muted">{announce}</p> : null}
      {rings.length === 0 ? (
        <p className="deployments-muted">{t('deployments.ring.none')}</p>
      ) : (
        <ol className="deployments-timeline" aria-busy={busy}>
          {rings.map((ring, index) => {
            const target = counts.get(ring.id);
            const change = ring.changeId ? plan.changes[ring.changeId] : undefined;
            const issues = ringIssues(validation.issues, ring.id);
            return (
              <li
                key={ring.id}
                className={`deployments-ring${dragId === ring.id ? ' deployments-ring-dragging' : ''}`}
                draggable={editable && !busy}
                onDragStart={(event) => {
                  setDragId(ring.id);
                  event.dataTransfer.effectAllowed = 'move';
                }}
                onDragEnd={() => setDragId(undefined)}
                onDragOver={(event) => {
                  if (dragId) event.preventDefault();
                }}
                onDrop={(event) => onDrop(event, index)}
              >
                <span className="deployments-ring-marker" aria-hidden="true">
                  {ring.position}
                </span>
                <article
                  className="deployments-ring-card"
                  aria-label={t('deployments.ring.cardLabel', {
                    position: ring.position,
                    name: ring.name,
                  })}
                >
                  <header>
                    <h3>
                      {ring.name}{' '}
                      {ring.position === 1 ? (
                        <StatusBadge tone="info">{t('deployments.ring.pilot')}</StatusBadge>
                      ) : null}
                    </h3>
                    {editable ? (
                      <div className="deployments-ring-tools">
                        <Button
                          aria-label={t('deployments.ring.moveUp', { name: ring.name })}
                          disabled={busy || index === 0}
                          onClick={() => void reorder(index, index - 1)}
                        >
                          ↑
                        </Button>
                        <Button
                          aria-label={t('deployments.ring.moveDown', { name: ring.name })}
                          disabled={busy || index === rings.length - 1}
                          onClick={() => void reorder(index, index + 1)}
                        >
                          ↓
                        </Button>
                        <Button onClick={() => setEditing(ring)}>
                          {t('deployments.ring.edit')}
                        </Button>
                        <Button variant="danger" onClick={() => setRemoving(ring)}>
                          {t('deployments.ring.remove')}
                        </Button>
                      </div>
                    ) : null}
                  </header>
                  <dl className="deployments-ring-facts">
                    <dt>{t('deployments.ring.targetSet')}</dt>
                    <dd>
                      <Link to={`/target-sets/${enc(ring.targetSetId)}`}>{ring.targetSetName}</Link>{' '}
                      <span className="deployments-ref">{ring.targetSetReference}</span>
                      <span className="deployments-muted">
                        {' · '}
                        {target
                          ? t(
                              target.truncated
                                ? 'deployments.ring.targetsTruncated'
                                : 'deployments.ring.targets',
                              {
                                count: target.matched,
                              },
                            )
                          : t('deployments.ring.targetsUnknown')}
                        {target?.incomplete || ring.incomplete
                          ? ` · ${t('deployments.ring.incomplete')}`
                          : ''}
                        {' · '}
                        {t('deployments.ring.cap', { count: ring.maxTargets })}
                      </span>
                    </dd>
                    <dt>{t('deployments.ring.gates')}</dt>
                    <dd>
                      {ring.approvalRequired
                        ? t('deployments.ring.gateApproval')
                        : t('deployments.ring.gateNoApproval')}
                      {' · '}
                      {t('deployments.ring.gateThreshold', {
                        percent: ring.successThresholdPercent,
                      })}
                      {ring.minFreshEvidencePercent != null
                        ? ` · ${t('deployments.ring.gateFresh', { percent: ring.minFreshEvidencePercent })}`
                        : ''}
                      {' · '}
                      {t('deployments.ring.soakLabel')} <Soak minutes={ring.soakMinutes} />
                    </dd>
                    <dt>{t('deployments.ring.window')}</dt>
                    <dd>
                      {ring.noWindowRequired ? (
                        t('deployments.ring.noWindowShort')
                      ) : ring.changeId ? (
                        <>
                          {changeLinks && !change?.hidden && change?.reference ? (
                            <Link to={`/changes/${enc(ring.changeId)}`}>{change.reference}</Link>
                          ) : (
                            <span>
                              {change?.reference && !change.hidden
                                ? change.reference
                                : t('deployments.window.restricted')}
                            </span>
                          )}{' '}
                          {change && !change.hidden ? <WindowPeriod change={change} /> : null}
                        </>
                      ) : (
                        t('deployments.window.none')
                      )}
                    </dd>
                  </dl>
                  {issues.length > 0 ? (
                    <ul className="deployments-issues deployments-ring-issues">
                      {issues.map((issue, i) => (
                        <li
                          key={`${issue.code}-${i}`}
                          className={issue.blocking ? 'is-blocking' : 'is-warning'}
                        >
                          <StatusBadge tone={issue.blocking ? 'danger' : 'warning'}>
                            {issue.blocking
                              ? t('deployments.validate.blockingOne')
                              : t('deployments.validate.warningOne')}
                          </StatusBadge>{' '}
                          <IssueText issue={issue} />
                        </li>
                      ))}
                    </ul>
                  ) : null}
                </article>
              </li>
            );
          })}
        </ol>
      )}
      {editing ? (
        <RingDialog
          plan={plan}
          ring={editing === 'new' ? undefined : editing}
          onClose={() => setEditing(undefined)}
          onDone={() => {
            setEditing(undefined);
            onChanged();
          }}
        />
      ) : null}
      {removing ? (
        <ConfirmDialog
          title={t('deployments.ring.removeTitle', { name: removing.name })}
          message={t('deployments.ring.removeText')}
          confirmLabel={t('deployments.ring.remove')}
          danger
          busy={busy}
          error={error ? t(errorMessageKey(error)) : undefined}
          onConfirm={() => void remove()}
          onCancel={() => setRemoving(undefined)}
        />
      ) : null}
    </Section>
  );
}

// ---- Validation panel ----

export function ValidationPanel({
  plan,
  validation,
  onValidate,
  validating,
  error,
}: {
  plan: DeploymentDetail;
  validation: PlanValidation;
  onValidate?: (() => void) | undefined;
  validating: boolean;
  error?: ApiError | undefined;
}) {
  const { t, locale } = useI18n();
  const headingId = useId();
  const blocking = blockingIssues(validation.issues);
  const warnings = warningIssues(validation.issues);
  const ringName = (id: string | null) => plan.rings.find((ring) => ring.id === id)?.name;
  return (
    <section className="deployments-card" aria-labelledby={headingId}>
      <div className="deployments-card-heading">
        <h2 id={headingId}>{t('deployments.validate.title')}</h2>
        {onValidate ? (
          <Button busy={validating} onClick={onValidate}>
            {t('deployments.validate.run')}
          </Button>
        ) : null}
      </div>
      {error ? <ApiErrorAlert error={error} /> : null}
      <p className="deployments-badges">
        <StatusBadge tone={validation.valid ? 'success' : 'danger'}>
          {validation.valid ? t('deployments.validate.valid') : t('deployments.validate.invalid')}
        </StatusBadge>
        {validation.highImpact ? <HighImpactBadge /> : null}
        {validation.incomplete ? (
          <StatusBadge tone="warning">{t('deployments.ring.incomplete')}</StatusBadge>
        ) : null}
        {validation.evaluated && validation.totalTargets != null ? (
          <span>{t('deployments.validate.total', { count: validation.totalTargets })}</span>
        ) : null}
        <span className="deployments-muted">
          {validation.evaluated
            ? t('deployments.validate.evaluated')
            : t('deployments.validate.structural')}{' '}
          <time dateTime={validation.validatedAt}>
            {formatDateTime(locale, validation.validatedAt)}
          </time>
        </span>
      </p>
      <div aria-live="polite">
        <h3>{t('deployments.validate.blocking', { count: blocking.length })}</h3>
        {blocking.length === 0 ? (
          <p className="deployments-muted">{t('deployments.validate.noBlocking')}</p>
        ) : (
          <ul className="deployments-issues">
            {blocking.map((issue, i) => (
              <li key={`${issue.code}-${i}`} className="is-blocking">
                <StatusBadge tone="danger">{t('deployments.validate.blockingOne')}</StatusBadge>{' '}
                <IssueText issue={issue} ringName={ringName(issue.ringId)} />
              </li>
            ))}
          </ul>
        )}
        <h3>{t('deployments.validate.warnings', { count: warnings.length })}</h3>
        {warnings.length === 0 ? (
          <p className="deployments-muted">{t('deployments.validate.noWarnings')}</p>
        ) : (
          <ul className="deployments-issues">
            {warnings.map((issue, i) => (
              <li key={`${issue.code}-${i}`} className="is-warning">
                <StatusBadge tone="warning">{t('deployments.validate.warningOne')}</StatusBadge>{' '}
                <IssueText issue={issue} ringName={ringName(issue.ringId)} />
              </li>
            ))}
          </ul>
        )}
        {validation.evaluated && validation.rings.length > 0 ? (
          <>
            <h3>{t('deployments.validate.ringTargets')}</h3>
            <ul className="deployments-ring-counts">
              {sortRings(plan.rings).map((ring) => {
                const entry = validation.rings.find((x) => x.ringId === ring.id);
                return (
                  <li key={ring.id}>
                    <span>
                      {ring.position}. {ring.name}
                    </span>
                    <strong>
                      {entry
                        ? `${entry.matched.toLocaleString(locale)}${entry.truncated ? '+' : ''}`
                        : '—'}
                    </strong>
                    {entry?.incomplete ? (
                      <span className="deployments-muted"> {t('deployments.ring.incomplete')}</span>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          </>
        ) : null}
      </div>
    </section>
  );
}
