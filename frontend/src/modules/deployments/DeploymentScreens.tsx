import { useEffect, useId, useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { useContextMenu } from '../../platform/ui/ContextMenu';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { FilterBar } from '../../platform/ui/FilterBar';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { Skeleton, StatusBadge, Tabs } from '../../platform/ui/Workspace';
import { softwareApi } from '../software/api';
import { Actor } from '../software/components';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { deploymentsApi } from './api';
import {
  ApprovalBadge,
  DeploymentStatusBadge,
  HighImpactBadge,
  Section,
  canViewSoftware,
} from './components';
import { codeKey, isRunStatus, planActions, planSteps, type PlanAction } from './helpers';
import {
  PlanFields,
  PlanFieldsDialog,
  RingTimeline,
  ValidationPanel,
  planFieldValues,
  planInput,
  type PlanFieldValues,
} from './PlanParts';
import { CancelDialog } from './CancelDialog';
import { hasReport } from './reportHelpers';
import { ReportView } from './ReportView';
import { RunView } from './RunView';
import {
  deploymentStatuses,
  type Deployment,
  type DeploymentDetail,
  type PlanValidation,
} from './types';

const enc = encodeURIComponent;

function IntentLabel({ plan }: { plan: Pick<Deployment, 'intent' | 'supersede'> }) {
  const { t } = useI18n();
  return (
    <>
      {t(codeKey('deployments.intent', plan.intent, 'deployments.unknownValue'))}
      {plan.supersede ? ` · ${t('deployments.field.supersedeShort')}` : ''}
    </>
  );
}

// ---- List ----

export function DeploymentsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const params = new URLSearchParams(window.location.search);
  const [status, setStatus] = useState(() => params.get('status') ?? '');
  const [productId, setProductId] = useState(() => params.get('productId') ?? '');
  useFilterQuery({ status, productId });
  const list = usePagedList(
    (cursor, signal) => deploymentsApi.list({ status, productId, versionId: '' }, cursor, signal),
    [status, productId],
  );
  const softwareAllowed = canViewSoftware(can);
  const products = useAsync(
    (signal) =>
      softwareAllowed
        ? softwareApi.products('', undefined, signal).then((page) => page.items)
        : Promise.resolve([]),
    [softwareAllowed],
  );
  const canManage = can('deployments.manage');
  const [cancelling, setCancelling] = useState<Deployment>();

  const productName = products.data?.find((product) => product.id === productId)?.name;
  const activeFilters = [
    ...(status
      ? [
          {
            key: 'status',
            label: t(codeKey('deployments.status', status, 'deployments.unknownValue')),
            onRemove: () => setStatus(''),
          },
        ]
      : []),
    ...(productId
      ? [
          {
            key: 'product',
            label: productName ?? t('deployments.list.productFilter'),
            onRemove: () => setProductId(''),
          },
        ]
      : []),
  ];

  const columns: Column<Deployment>[] = [
    {
      key: 'name',
      header: t('deployments.field.name'),
      sortValue: (plan) => plan.name,
      render: (plan) => (
        <span className="deployments-name-cell">
          <Link to={`/deployments/${enc(plan.id)}`}>{plan.name}</Link>
          <span className="deployments-ref">{plan.reference}</span>
        </span>
      ),
    },
    {
      key: 'software',
      header: t('deployments.list.software'),
      sortValue: (plan) => `${plan.productName} ${plan.productVersion}`,
      render: (plan) => (
        <>
          {plan.productName} <span className="deployments-mono">{plan.productVersion}</span>
        </>
      ),
    },
    {
      key: 'intent',
      header: t('deployments.field.intent'),
      render: (plan) => <IntentLabel plan={plan} />,
    },
    {
      key: 'status',
      header: t('deployments.list.status'),
      sortValue: (plan) => plan.status,
      render: (plan) => (
        <span className="deployments-badges">
          <DeploymentStatusBadge status={plan.status} />
          {plan.highImpact ? <HighImpactBadge /> : null}
        </span>
      ),
    },
    {
      key: 'rings',
      header: t('deployments.list.rings'),
      sortValue: (plan) => plan.ringCount ?? -1,
      render: (plan) => (plan.ringCount !== undefined ? plan.ringCount : '—'),
    },
    {
      key: 'updated',
      header: t('deployments.updatedAt'),
      sortValue: (plan) => plan.updatedAt,
      render: (plan) => <TableDate value={plan.updatedAt} />,
    },
  ];

  return (
    <div className="deployments-workspace">
      <PageHeader
        eyebrow={t('deployments.eyebrow')}
        title={t('deployments.list.title')}
        intro={
          can('deployments.view') || canManage || can('deployments.execute')
            ? t('deployments.list.intro')
            : t('deployments.list.introOwn')
        }
        actions={
          canManage ? (
            <Link className="btn btn-primary" to="/deployments/new">
              {t('deployments.list.new')}
            </Link>
          ) : undefined
        }
      />
      <FilterBar
        activeFilters={activeFilters}
        onClear={() => {
          setStatus('');
          setProductId('');
        }}
      >
        <Select
          label={t('deployments.list.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...deploymentStatuses.map((value) => ({
              value,
              label: t(codeKey('deployments.status', value, 'deployments.unknownValue')),
            })),
          ]}
        />
        {softwareAllowed ? (
          <Select
            label={t('deployments.list.productFilter')}
            value={productId}
            disabled={products.loading}
            onChange={(event) => setProductId(event.target.value)}
            options={[
              { value: '', label: t('filters.all') },
              ...(products.data ?? []).map((product) => ({
                value: product.id,
                label: product.name,
              })),
            ]}
          />
        ) : null}
      </FilterBar>
      <DataTable
        caption={t('deployments.list.title')}
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        columns={columns}
        rows={list.items}
        rowKey={(plan) => plan.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('deployments.list.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
        rowActions={(plan) => [
          {
            id: 'open',
            label: t('deployments.list.open'),
            onSelect: () => navigate(`/deployments/${enc(plan.id)}`),
          },
          ...(canManage
            ? [
                {
                  id: 'cancel',
                  label: t('deployments.action.cancel'),
                  danger: true,
                  ...(plan.status === 'cancelled'
                    ? { disabledReason: t('deployments.reason.alreadyCancelled') }
                    : {}),
                  onSelect: () => setCancelling(plan),
                },
              ]
            : []),
        ]}
      />
      {cancelling ? (
        <CancelDialog
          plan={cancelling}
          onClose={() => setCancelling(undefined)}
          onDone={() => {
            setCancelling(undefined);
            list.reload();
          }}
        />
      ) : null}
    </div>
  );
}

// ---- Dialogs ----

function SubmitDialog({
  plan,
  onClose,
  onDone,
  onFailed,
}: {
  plan: DeploymentDetail;
  onClose: () => void;
  onDone: () => void;
  onFailed: (error: ApiError) => void;
}) {
  const { t } = useI18n();
  const [type, setType] = useState<'user' | 'team'>('user');
  const [approver, setApprover] = useState<Assignee | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!approver) return;
    setBusy(true);
    setError(undefined);
    try {
      await deploymentsApi.submit(plan.id, { type, id: approver.id }, plan.version);
      onDone();
    } catch (cause) {
      const apiError = asApiError(cause);
      setError(apiError);
      onFailed(apiError);
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('deployments.submit.title')} onClose={onClose} wide>
      <form className="form" onSubmit={(event) => void submit(event)}>
        <div className="dialog-body">
          <p>{t('deployments.submit.text')}</p>
          <p className="deployments-muted">{t('deployments.submit.excluded')}</p>
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
            {t('deployments.action.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function ScheduleDialog({
  plan,
  onClose,
  onDone,
  onFailed,
}: {
  plan: DeploymentDetail;
  onClose: () => void;
  onDone: () => void;
  onFailed: (error: ApiError) => void;
}) {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const confirm = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await deploymentsApi.schedule(plan.id, plan.version);
      onDone();
    } catch (cause) {
      const apiError = asApiError(cause);
      setError(apiError);
      onFailed(apiError);
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('deployments.schedule.title')} onClose={onClose}>
      <div className="dialog-body">
        <p>{t('deployments.schedule.text', { rings: plan.rings.length })}</p>
        <p className="deployments-muted">{t('deployments.schedule.noExecution')}</p>
      </div>
      {error ? <ApiErrorAlert error={error} /> : null}
      <div className="dialog-actions">
        <Button onClick={onClose} autoFocus>
          {t('action.cancel')}
        </Button>
        <Button variant="primary" busy={busy} onClick={() => void confirm()}>
          {t('deployments.action.schedule')}
        </Button>
      </div>
    </Dialog>
  );
}

// ---- Plan workspace ----

export function DeploymentPlanScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const menu = useContextMenu();
  const noteId = useId();
  const detail = useAsync((signal) => deploymentsApi.get(id, signal), [id]);
  const plan = detail.data;
  const [full, setFull] = useState<PlanValidation>();
  const [validating, setValidating] = useState(false);
  const [validateError, setValidateError] = useState<ApiError>();
  const [dialog, setDialog] = useState<'submit' | 'schedule' | 'cancel' | 'edit'>();
  const [tab, setTab] = useState<'plan' | 'report'>('plan');
  const [lastError, setLastError] = useState<string>();
  const [notice, setNotice] = useState('');

  // A newer plan version invalidates an earlier full validation.
  const planVersion = plan?.version;
  useEffect(() => {
    setFull(undefined);
  }, [planVersion]);

  const runValidate = async () => {
    setValidating(true);
    setValidateError(undefined);
    try {
      setFull(await deploymentsApi.validate(id));
      // A fresh validation answers plan_changed for drafts; an approved plan stays changed.
      setLastError((code) =>
        code === 'endpoints.plan_changed' && plan?.status === 'approved' ? code : undefined,
      );
    } catch (cause) {
      setValidateError(asApiError(cause));
    } finally {
      setValidating(false);
    }
  };

  const failed = (error: ApiError) => {
    setLastError(error.code);
    if (error.code === 'endpoints.deployment_invalid') void runValidate();
  };
  const done = (message: MessageKey) => {
    setDialog(undefined);
    setLastError(undefined);
    setNotice(t(message));
    detail.reload();
  };

  if (detail.error) return <ApiErrorAlert error={detail.error} onRetry={detail.reload} />;
  if (!plan) return <Skeleton lines={8} />;

  const validation = full ?? plan.validation;
  // The stored flag of a draft is a snapshot; the validation recomputes it.
  const highImpact = validation.highImpact;
  const actions = planActions({ ...plan, highImpact, validation }, can, lastError);
  const primary = actions.find((action) => action.id !== 'cancel' && !action.disabledReason);
  const editable = plan.status === 'draft' && can('deployments.manage');
  const menuItems = [
    ...(editable
      ? [
          {
            id: 'edit',
            label: t('deployments.plan.editDetails'),
            onSelect: () => setDialog('edit'),
          },
        ]
      : []),
    ...actions
      .filter((action) => action.id === 'cancel')
      .map((action) => ({
        id: action.id,
        label: t('deployments.action.cancel'),
        danger: true,
        onSelect: () => setDialog('cancel'),
      })),
  ];
  const steps = planSteps(plan.status, highImpact || (plan.status !== 'draft' && plan.highImpact));
  const actionLabel = (action: PlanAction) => t(`deployments.action.${action.id}`);
  const reason = validation.highImpactReason;
  const isRun = isRunStatus(plan.status);
  const reportAllowed =
    hasReport(plan.status, plan.scheduledAt) &&
    (can('deployments.view') || can('deployments.manage') || can('deployments.execute'));
  const openApproval = plan.approvals.find((approval) => approval.status === 'pending');

  return (
    <div className="deployments-workspace">
      <PageHeader
        eyebrow={plan.reference}
        title={plan.name}
        actions={
          <>
            <Link className="btn btn-secondary" to="/deployments">
              {t('deployments.plan.back')}
            </Link>
            {primary ? (
              <Button variant="primary" onClick={() => setDialog(primary.id)}>
                {actionLabel(primary)}
              </Button>
            ) : null}
            {menuItems.length > 0 ? (
              <Button
                aria-label={t('deployments.plan.more')}
                aria-haspopup="menu"
                onClick={(event) =>
                  menu.openAtElement(menuItems, event.currentTarget, t('deployments.plan.more'))
                }
              >
                •••
              </Button>
            ) : null}
          </>
        }
      />
      {menu.menu}
      <p className="deployments-badges">
        <DeploymentStatusBadge status={plan.status} />
        {highImpact ? <HighImpactBadge /> : null}
        <StatusBadge tone="neutral">
          <IntentLabel plan={plan} />
        </StatusBadge>
        <span>
          {plan.productName} <span className="deployments-mono">{plan.productVersion}</span>
          {canViewSoftware(can) ? (
            <>
              {' · '}
              <Link to={`/software/versions/${enc(plan.softwareVersionId)}`}>
                {t('deployments.plan.openVersion')}
              </Link>
            </>
          ) : null}
        </span>
      </p>
      {notice ? <Alert kind="success">{notice}</Alert> : null}
      {plan.statusReason && !isRun ? (
        <Alert kind={plan.statusReason === 'approval_rejected' ? 'warning' : 'info'}>
          {t(
            codeKey(
              'deployments.statusReason',
              plan.statusReason,
              'deployments.statusReason.other',
            ),
            {
              code: plan.statusReason,
            },
          )}
        </Alert>
      ) : null}
      {highImpact ? (
        <Alert kind="warning">
          {t('deployments.plan.highImpactNotice')}{' '}
          {t(
            codeKey('deployments.highImpactReason', reason, 'deployments.highImpactReason.unknown'),
          )}
        </Alert>
      ) : null}

      {reportAllowed ? (
        <Tabs
          idPrefix="deployment-tabs"
          items={[
            { id: 'plan', label: t('deployments.tab.plan') },
            { id: 'report', label: t('deployments.tab.report') },
          ]}
          active={tab}
          onChange={(id) => setTab(id === 'report' ? 'report' : 'plan')}
        />
      ) : null}
      {tab === 'report' && reportAllowed ? (
        <ReportView plan={plan} />
      ) : (
        <>
          {isRun ? <RunView plan={plan} onChanged={detail.reload} /> : null}

          <section className="deployments-lifecycle" aria-labelledby={`${noteId}-steps`}>
            <div className="deployments-card-heading">
              <h2 id={`${noteId}-steps`}>{t('deployments.plan.lifecycle')}</h2>
              <DeploymentStatusBadge status={plan.status} />
            </div>
            <ol>
              {steps.map((step, index) => (
                <li
                  key={step.id}
                  className={`deployments-step deployments-step-${step.state}`}
                  aria-current={
                    step.id === plan.status ||
                    (step.id === 'submitted' && plan.status === 'pending_approval')
                      ? 'step'
                      : undefined
                  }
                >
                  <span aria-hidden="true">
                    {step.state === 'failed' ? '−' : String(index + 1).padStart(2, '0')}
                  </span>
                  {t(`deployments.step.${step.id}` as MessageKey)}
                  <span className="visually-hidden">
                    {' '}
                    {t(`deployments.stepState.${step.state}`)}
                  </span>
                </li>
              ))}
            </ol>
            {actions.length > 0 ? (
              <div className="deployments-action-bar">
                {actions.map((action) => (
                  <Button
                    key={action.id}
                    variant={
                      action.id === 'cancel'
                        ? 'danger'
                        : action === primary
                          ? 'primary'
                          : 'secondary'
                    }
                    disabled={action.disabledReason !== undefined}
                    aria-describedby={action.disabledReason ? `${noteId}-${action.id}` : undefined}
                    onClick={() => setDialog(action.id)}
                  >
                    {actionLabel(action)}
                  </Button>
                ))}
              </div>
            ) : isRun ? null : (
              <p className="deployments-muted">
                {t(codeKey('deployments.statusHint', plan.status, 'deployments.statusHint.none'))}
              </p>
            )}
            {actions
              .filter((action) => action.disabledReason)
              .map((action) => (
                <p
                  key={action.id}
                  id={`${noteId}-${action.id}`}
                  className="deployments-disabled-note"
                >
                  {actionLabel(action)}: {t(action.disabledReason as MessageKey)}
                </p>
              ))}
          </section>

          <div className="deployments-detail-grid">
            <div className="deployments-detail-main">
              <RingTimeline
                plan={plan}
                validation={validation}
                editable={editable}
                onChanged={detail.reload}
              />
              <ValidationPanel
                plan={plan}
                validation={validation}
                validating={validating}
                error={validateError}
                onValidate={can('deployments.manage') ? () => void runValidate() : undefined}
              />
            </div>
            <aside className="deployments-detail-side" aria-label={t('deployments.plan.context')}>
              <Section title={t('deployments.plan.facts')}>
                <dl className="deployments-facts">
                  <dt>{t('deployments.list.software')}</dt>
                  <dd>
                    {plan.productName} {plan.productVersion}
                  </dd>
                  <dt>{t('deployments.field.intent')}</dt>
                  <dd>
                    <IntentLabel plan={plan} />
                  </dd>
                  <dt>{t('deployments.field.owner')}</dt>
                  <dd>
                    <Actor userId={plan.ownerUserId} />
                  </dd>
                  <dt>{t('deployments.plan.createdAt')}</dt>
                  <dd>
                    <time dateTime={plan.createdAt}>{formatDateTime(locale, plan.createdAt)}</time>
                  </dd>
                  {plan.scheduledAt ? (
                    <>
                      <dt>{t('deployments.plan.scheduledAt')}</dt>
                      <dd>
                        <time dateTime={plan.scheduledAt}>
                          {formatDateTime(locale, plan.scheduledAt)}
                        </time>
                      </dd>
                    </>
                  ) : null}
                  {plan.planSha256 ? (
                    <>
                      <dt>{t('deployments.plan.hash')}</dt>
                      <dd>
                        <code title={plan.planSha256}>{plan.planSha256.slice(0, 12)}…</code>
                      </dd>
                    </>
                  ) : null}
                </dl>
              </Section>
              <Section title={t('deployments.approval.title')}>
                {!highImpact && plan.approvals.length === 0 ? (
                  <p className="deployments-muted">{t('deployments.approval.notNeeded')}</p>
                ) : plan.approvals.length === 0 ? (
                  <p className="deployments-muted">{t('deployments.approval.notRequested')}</p>
                ) : (
                  <ul className="deployments-approvals">
                    {plan.approvals.map((approval) => (
                      <li key={approval.id}>
                        <ApprovalBadge status={approval.status} />{' '}
                        {approval.approverTeamId
                          ? t('deployments.approval.team')
                          : t('deployments.approval.user')}
                        {approval.decidedAt ? (
                          <>
                            {' · '}
                            <time dateTime={approval.decidedAt}>
                              {formatDateTime(locale, approval.decidedAt)}
                            </time>
                          </>
                        ) : null}{' '}
                        <Link to={`/approvals/${enc(approval.id)}`}>
                          {t('deployments.approval.open')}
                        </Link>
                      </li>
                    ))}
                  </ul>
                )}
                {plan.status === 'approved' ? (
                  <p className="deployments-muted">{t('deployments.approval.bound')}</p>
                ) : null}
                {openApproval ? (
                  <p className="deployments-muted">{t('deployments.approval.waiting')}</p>
                ) : null}
              </Section>
              <Section title={t('deployments.plan.history')}>
                {plan.transitions.length === 0 ? (
                  <p className="deployments-muted">—</p>
                ) : (
                  <ol className="deployments-history">
                    {[...plan.transitions].reverse().map((transition, index) => (
                      <li key={`${transition.createdAt}-${index}`}>
                        <strong>
                          {t(
                            codeKey(
                              'deployments.operation',
                              transition.operation,
                              'deployments.unknownValue',
                            ),
                          )}
                        </strong>{' '}
                        <span className="deployments-muted">
                          <Actor userId={transition.actorUserId} system={transition.actorSystem} />
                          {' · '}
                          <time dateTime={transition.createdAt}>
                            {formatDateTime(locale, transition.createdAt)}
                          </time>
                        </span>
                        {transition.reason ? (
                          <span className="deployments-muted">
                            {' · '}
                            {t(
                              codeKey(
                                'deployments.cancelReason',
                                transition.reason,
                                'deployments.statusReason.other',
                              ),
                              {
                                code: transition.reason,
                              },
                            )}
                          </span>
                        ) : null}
                      </li>
                    ))}
                  </ol>
                )}
              </Section>
            </aside>
          </div>
        </>
      )}
      {dialog === 'submit' ? (
        <SubmitDialog
          plan={plan}
          onClose={() => setDialog(undefined)}
          onDone={() => done('deployments.notice.submitted')}
          onFailed={failed}
        />
      ) : null}
      {dialog === 'schedule' ? (
        <ScheduleDialog
          plan={plan}
          onClose={() => setDialog(undefined)}
          onDone={() => done('deployments.notice.scheduled')}
          onFailed={failed}
        />
      ) : null}
      {dialog === 'cancel' ? (
        <CancelDialog
          plan={plan}
          onClose={() => setDialog(undefined)}
          onDone={() => done('deployments.notice.cancelled')}
        />
      ) : null}
      {dialog === 'edit' ? (
        <PlanFieldsDialog
          plan={plan}
          onClose={() => setDialog(undefined)}
          onDone={() => done('deployments.notice.saved')}
        />
      ) : null}
    </div>
  );
}

// ---- Create wizard ----

const wizardSteps = ['intent', 'rings', 'review'] as const;
type WizardStep = (typeof wizardSteps)[number];

/**
 * Three steps: version and intent (creates or updates the draft), target sets and rings (edits the
 * draft's rings), review and validate. The draft id stays in the URL so the wizard can be resumed.
 */
export function DeploymentWizardScreen() {
  const { t } = useI18n();
  const query = new URLSearchParams(window.location.search);
  const [planId, setPlanId] = useState(() => query.get('id') ?? '');
  const [step, setStep] = useState<WizardStep>(() =>
    query.get('id') && wizardSteps.includes(query.get('step') as WizardStep)
      ? (query.get('step') as WizardStep)
      : 'intent',
  );
  const detail = useAsync(
    (signal) => (planId ? deploymentsApi.get(planId, signal) : Promise.resolve(undefined)),
    [planId],
  );
  const plan = detail.data;
  const [values, setValues] = useState<PlanFieldValues>(() => planFieldValues(undefined));
  const [adopted, setAdopted] = useState('');
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [validation, setValidation] = useState<PlanValidation>();
  const [validateError, setValidateError] = useState<ApiError>();
  const headingId = useId();

  if (plan && adopted !== plan.id) {
    setAdopted(plan.id);
    setValues(planFieldValues(plan));
  }

  const go = (next: WizardStep, id = planId) => {
    setStep(next);
    window.history.replaceState(null, '', `/deployments/new?id=${enc(id)}&step=${next}`);
  };

  const saveIntent = async (event: FormEvent) => {
    event.preventDefault();
    setTried(true);
    if (!values.name.trim() || !values.versionId) return;
    setBusy(true);
    setError(undefined);
    try {
      if (plan) {
        await deploymentsApi.update(plan.id, planInput(values), plan.version);
        detail.reload();
        go('rings');
      } else {
        const created = await deploymentsApi.create(planInput(values));
        setPlanId(created.id);
        go('rings', created.id);
      }
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const validate = async () => {
    if (!planId) return;
    setValidateError(undefined);
    setBusy(true);
    try {
      setValidation(await deploymentsApi.validate(planId));
    } catch (cause) {
      setValidateError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const stepIndex = wizardSteps.indexOf(step);
  const editable = !plan || plan.status === 'draft';

  return (
    <div className="deployments-workspace">
      <PageHeader
        eyebrow={plan?.reference ?? t('deployments.eyebrow')}
        title={t('deployments.wizard.title')}
        intro={t('deployments.wizard.intro')}
        actions={
          <Link
            className="btn btn-secondary"
            to={plan ? `/deployments/${enc(plan.id)}` : '/deployments'}
          >
            {plan ? t('deployments.wizard.toWorkspace') : t('deployments.plan.back')}
          </Link>
        }
      />
      <nav aria-labelledby={headingId} className="deployments-lifecycle deployments-wizard-steps">
        <h2 id={headingId} className="visually-hidden">
          {t('deployments.wizard.steps')}
        </h2>
        <ol>
          {wizardSteps.map((id, index) => (
            <li
              key={id}
              className={`deployments-step deployments-step-${index < stepIndex ? 'done' : index === stepIndex ? 'current' : 'upcoming'}`}
              aria-current={id === step ? 'step' : undefined}
            >
              <span aria-hidden="true">{String(index + 1).padStart(2, '0')}</span>
              {index <= stepIndex || (plan && index > 0) ? (
                <button
                  type="button"
                  className="deployments-step-button"
                  disabled={index > 0 && !plan}
                  onClick={() => go(id)}
                >
                  {t(`deployments.wizard.step.${id}`)}
                </button>
              ) : (
                t(`deployments.wizard.step.${id}`)
              )}
            </li>
          ))}
        </ol>
      </nav>
      {planId && detail.error ? (
        <ApiErrorAlert error={detail.error} onRetry={detail.reload} />
      ) : null}
      {planId && !plan && !detail.error ? <Skeleton lines={6} /> : null}
      {plan && !editable ? <Alert kind="info">{t('deployments.wizard.notDraft')}</Alert> : null}

      {step === 'intent' && (!planId || plan) ? (
        <form
          className="deployments-card form"
          onSubmit={(event) => void saveIntent(event)}
          noValidate
        >
          <h2>{t('deployments.wizard.step.intent')}</h2>
          <p className="deployments-muted">{t('deployments.wizard.intentIntro')}</p>
          <fieldset className="deployments-fieldset" disabled={!editable}>
            <legend className="visually-hidden">{t('deployments.wizard.step.intent')}</legend>
            <PlanFields values={values} onChange={setValues} showErrors={tried} />
          </fieldset>
          {error ? <ApiErrorAlert error={error} /> : null}
          <div className="deployments-wizard-actions">
            <Button type="submit" variant="primary" busy={busy} disabled={!editable}>
              {plan ? t('deployments.wizard.saveNext') : t('deployments.wizard.createNext')}
            </Button>
          </div>
        </form>
      ) : null}

      {step === 'rings' && plan ? (
        <>
          <p className="deployments-muted">{t('deployments.wizard.ringsIntro')}</p>
          <RingTimeline
            plan={plan}
            validation={plan.validation}
            editable={editable}
            onChanged={detail.reload}
          />
          <div className="deployments-wizard-actions">
            <Button onClick={() => go('intent')}>{t('deployments.wizard.back')}</Button>
            <Button
              variant="primary"
              onClick={() => {
                go('review');
                void validate();
              }}
            >
              {t('deployments.wizard.toReview')}
            </Button>
          </div>
        </>
      ) : null}

      {step === 'review' && plan ? (
        <>
          <Section title={t('deployments.wizard.summary')}>
            <dl className="deployments-facts">
              <dt>{t('deployments.field.name')}</dt>
              <dd>{plan.name}</dd>
              <dt>{t('deployments.list.software')}</dt>
              <dd>
                {plan.productName} {plan.productVersion}
              </dd>
              <dt>{t('deployments.field.intent')}</dt>
              <dd>
                <IntentLabel plan={plan} />
              </dd>
              <dt>{t('deployments.list.rings')}</dt>
              <dd>{plan.rings.length}</dd>
            </dl>
            {(validation ?? plan.validation).highImpact ? (
              <Alert kind="warning">{t('deployments.wizard.highImpactNext')}</Alert>
            ) : null}
          </Section>
          <ValidationPanel
            plan={plan}
            validation={validation ?? plan.validation}
            validating={busy}
            error={validateError}
            onValidate={() => void validate()}
          />
          <div className="deployments-wizard-actions">
            <Button onClick={() => go('rings')}>{t('deployments.wizard.back')}</Button>
            <Link className="btn btn-primary" to={`/deployments/${enc(plan.id)}`}>
              {t('deployments.wizard.finish')}
            </Link>
          </div>
        </>
      ) : null}
    </div>
  );
}
