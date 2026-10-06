import { useId, useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Table } from '../../platform/ui/Table';
import { TableDate } from '../../platform/ui/TableDate';
import { Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { softwareApi } from './api';
import {
  Actor,
  HashChip,
  HashMismatchBadge,
  AttemptCounts,
  PublishedAfterRevokeBadge,
  OperationDialog,
  PackageStatusBadge,
  VersionStatusBadge,
} from './components';
import {
  approvalSteps,
  publishBlocker,
  validateRegister,
  versionActions,
  type RegisterErrors,
  type VersionAction,
} from './helpers';
import {
  versionReasons,
  type SoftwareApproval,
  type SoftwarePackage,
  type SoftwareVersion,
} from './types';

const enc = encodeURIComponent;

type Pending =
  { kind: 'version'; action: VersionAction['id'] } | { kind: 'publish'; pkg: SoftwarePackage };

export function SoftwareVersionDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const noteId = useId();
  const detail = useAsync((signal) => softwareApi.version(id, signal), [id]);
  const [pending, setPending] = useState<Pending>();
  const data = detail.data;
  const version = data?.version;
  const actions = version ? versionActions(version, session?.userId, can) : [];
  const firstEnabled = actions.find((action) => !action.disabledReason);
  const done = () => {
    setPending(undefined);
    detail.reload();
  };

  return (
    <div className="software-workspace">
      <PageHeader
        eyebrow={version?.productName ?? t('software.eyebrow')}
        title={
          version
            ? t('software.version.title', {
                product: version.productName,
                version: version.productVersion,
              })
            : t('software.version.detail')
        }
        actions={
          <Link className="btn btn-secondary" to="/software">
            {t('software.back')}
          </Link>
        }
      />
      {detail.error ? <ApiErrorAlert error={detail.error} onRetry={detail.reload} /> : null}
      {detail.loading && !data ? <Skeleton lines={8} /> : null}
      {data && version ? (
        <>
          <section className="software-lifecycle" aria-labelledby={`${noteId}-lifecycle`}>
            <div className="software-lifecycle-heading">
              <h2 id={`${noteId}-lifecycle`}>{t('software.approval.title')}</h2>
              <VersionStatusBadge status={version.approvalStatus} />
            </div>
            <ol>
              {approvalSteps(version.approvalStatus).map((step, index) => (
                <li
                  key={step.id}
                  className={`software-step software-step-${step.state}`}
                  aria-current={step.id === version.approvalStatus ? 'step' : undefined}
                >
                  <span aria-hidden="true">
                    {step.state === 'failed' ? '!' : String(index + 1).padStart(2, '0')}
                  </span>
                  {t(`software.versionStatus.${step.id}` as MessageKey)}
                  <span className="visually-hidden">
                    {' '}
                    {t(`software.step.${step.state}` as MessageKey)}
                  </span>
                </li>
              ))}
            </ol>
            {actions.length > 0 ? (
              <div className="software-action-bar">
                {actions.map((action) => (
                  <Button
                    key={action.id}
                    variant={
                      action.id === 'reject' || action.id === 'revoke'
                        ? 'danger'
                        : action === firstEnabled
                          ? 'primary'
                          : 'secondary'
                    }
                    disabled={action.disabledReason !== undefined}
                    aria-describedby={action.disabledReason ? `${noteId}-${action.id}` : undefined}
                    onClick={() => setPending({ kind: 'version', action: action.id })}
                  >
                    {t(`software.versionOp.${action.id}` as MessageKey)}
                  </Button>
                ))}
              </div>
            ) : (
              <p className="software-muted">
                {t(`software.versionStatusHint.${version.approvalStatus}` as MessageKey)}
              </p>
            )}
            {actions
              .filter((action) => action.disabledReason)
              .map((action) => (
                <p key={action.id} id={`${noteId}-${action.id}`} className="software-disabled-note">
                  {t(action.disabledReason as MessageKey)}
                </p>
              ))}
          </section>
          <div className="software-detail-grid">
            <div className="software-detail-main">
              <BindingCard version={version} />
              <PackagesCard
                version={version}
                packages={data.packages}
                onPublish={(pkg) => setPending({ kind: 'publish', pkg })}
              />
            </div>
            <aside className="software-detail-side">
              <HistoryCard approvals={data.approvals} />
            </aside>
          </div>
          {pending?.kind === 'version' ? (
            <VersionOperationDialog
              version={version}
              action={pending.action}
              onClose={() => setPending(undefined)}
              onDone={done}
            />
          ) : null}
          {pending?.kind === 'publish' ? (
            <PublishDialog pkg={pending.pkg} onClose={() => setPending(undefined)} onDone={done} />
          ) : null}
        </>
      ) : null}
    </div>
  );
}

function BindingCard({ version }: { version: SoftwareVersion }) {
  const { t } = useI18n();
  return (
    <section className="software-card software-facts">
      <h2>{t('software.binding.title')}</h2>
      <p className="software-muted">{t('software.binding.intro')}</p>
      <dl>
        <dt>{t('software.version.number')}</dt>
        <dd>{version.productVersion}</dd>
        <dt>{t('software.publisher')}</dt>
        <dd>{version.publisher ?? '—'}</dd>
        <dt>{t('software.binding.installerSha256')}</dt>
        <dd>
          <HashChip value={version.installerSha256} label={t('software.binding.installerSha256')} />
        </dd>
        <dt>{t('software.binding.installerUrl')}</dt>
        <dd className="software-text-break">
          {version.installerUrl ?? (
            <span className="software-muted">{t('software.binding.redacted')}</span>
          )}
        </dd>
        <dt>{t('software.binding.installCommand')}</dt>
        <dd>
          {version.installCommand !== null ? (
            <code className="software-code">{version.installCommand}</code>
          ) : (
            <span className="software-muted">{t('software.binding.redacted')}</span>
          )}
          <HashChip
            short
            value={version.installCommandSha256}
            label={t('software.binding.installCommandSha256')}
          />
        </dd>
        <dt>{t('software.binding.detectionRule')}</dt>
        <dd>
          {version.detectionRule !== null ? (
            <pre className="software-code">{version.detectionRule}</pre>
          ) : (
            <span className="software-muted">{t('software.binding.redacted')}</span>
          )}
          <HashChip
            short
            value={version.detectionRuleSha256}
            label={t('software.binding.detectionRuleSha256')}
          />
        </dd>
        <dt>{t('software.binding.bindingSha256')}</dt>
        <dd>
          <HashChip
            short
            value={version.bindingSha256}
            label={t('software.binding.bindingSha256')}
          />
        </dd>
        <dt>{t('software.version.registered')}</dt>
        <dd>
          <Actor userId={version.registeredBy} /> · <TableDate value={version.createdAt} />
        </dd>
        <dt>{t('software.version.requested')}</dt>
        <dd>
          {version.requestedAt ? (
            <>
              <Actor userId={version.requestedBy} /> · <TableDate value={version.requestedAt} />
            </>
          ) : (
            '—'
          )}
        </dd>
        <dt>{t('software.version.decided')}</dt>
        <dd>
          {version.decidedAt ? (
            <>
              <Actor userId={version.decidedBy} /> · <TableDate value={version.decidedAt} />
              {version.approvalReason
                ? ` · ${t(`software.reasonCode.${version.approvalReason}` as MessageKey)}`
                : ''}
            </>
          ) : (
            '—'
          )}
        </dd>
      </dl>
    </section>
  );
}

function PackagesCard({
  version,
  packages,
  onPublish,
}: {
  version: SoftwareVersion;
  packages: SoftwarePackage[];
  onPublish: (pkg: SoftwarePackage) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const noteId = useId();
  const canPackage = can('software.package');
  const canSeeArtifacts = can('endpoint.management.view') || can('endpoints.manage');
  return (
    <section className="software-card">
      <h2>{t('software.packages.title')}</h2>
      {packages.length === 0 ? (
        <p className="software-muted">
          {version.approvalStatus === 'approved'
            ? t('software.packages.noneApproved')
            : t('software.packages.noneYet')}
        </p>
      ) : (
        <Table>
          <caption className="visually-hidden">{t('software.packages.title')}</caption>
          <thead>
            <tr>
              <th scope="col">{t('software.package.provider')}</th>
              <th scope="col">{t('software.package.status')}</th>
              <th scope="col">{t('software.package.reportedHash')}</th>
              <th scope="col">{t('software.package.artifact')}</th>
              <th scope="col">{t('software.package.observedAt')}</th>
              {canPackage ? <th scope="col">{t('contextMenu.actions')}</th> : null}
            </tr>
          </thead>
          <tbody>
            {packages.map((pkg) => {
              const blocker = publishBlocker(pkg);
              return (
                <tr key={pkg.id}>
                  <td>{pkg.provider}</td>
                  <td>
                    <span className="software-badges">
                      <PackageStatusBadge status={pkg.status} />
                      {pkg.hashMismatch ? <HashMismatchBadge /> : null}
                      {pkg.publishedAfterRevoke ? <PublishedAfterRevokeBadge /> : null}
                      {pkg.versionRevoked ? (
                        <StatusBadge tone="danger">
                          {t('software.package.versionRevoked')}
                        </StatusBadge>
                      ) : null}
                      {pkg.productBlocked ? (
                        <StatusBadge tone="danger">
                          {t('software.package.productBlocked')}
                        </StatusBadge>
                      ) : null}
                    </span>
                    <AttemptCounts
                      packageAttempt={pkg.packageAttempt}
                      publishAttempt={pkg.publishAttempt}
                    />
                  </td>
                  <td>
                    {pkg.installerSha256 ? (
                      <HashChip
                        short
                        value={pkg.installerSha256}
                        label={t('software.package.reportedHash')}
                      />
                    ) : (
                      t('software.package.notReported')
                    )}
                  </td>
                  <td>
                    {pkg.managementArtifactId && canSeeArtifacts ? (
                      <Link to={`/management-artifacts/${enc(pkg.managementArtifactId)}`}>
                        {t('software.package.openArtifact')}
                      </Link>
                    ) : pkg.managementArtifactExternalId ? (
                      t('software.package.artifactPending')
                    ) : (
                      '—'
                    )}
                  </td>
                  <td>
                    <TableDate value={pkg.observedAt} />
                    <span className="software-muted">
                      {' '}
                      {t(`software.source.${pkg.source}` as MessageKey)}
                    </span>
                  </td>
                  {canPackage ? (
                    <td>
                      <Button
                        disabled={blocker !== undefined}
                        aria-describedby={blocker ? `${noteId}-${pkg.id}` : undefined}
                        onClick={() => onPublish(pkg)}
                      >
                        {t('software.packageOp.publish')}
                      </Button>
                      {blocker ? (
                        <span id={`${noteId}-${pkg.id}`} className="software-disabled-note">
                          {t(blocker)}
                        </span>
                      ) : null}
                    </td>
                  ) : null}
                </tr>
              );
            })}
          </tbody>
        </Table>
      )}
      {packages.some((pkg) => pkg.publishedAfterRevoke) ? (
        <Alert kind="warning">{t('software.package.publishedAfterRevokeHint')}</Alert>
      ) : null}
      {packages.some((pkg) => pkg.hashMismatch) ? (
        <Alert kind="warning">{t('software.package.hashMismatchHint')}</Alert>
      ) : null}
    </section>
  );
}

function HistoryCard({ approvals }: { approvals: SoftwareApproval[] }) {
  const { t } = useI18n();
  return (
    <section className="software-card">
      <h2>{t('software.history.title')}</h2>
      {approvals.length === 0 ? (
        <p className="software-muted">{t('software.history.empty')}</p>
      ) : (
        <ol className="software-timeline">
          {approvals.map((entry) => (
            <li key={entry.id} className={`software-timeline-${entry.toStatus}`}>
              <p className="software-timeline-title">
                {t(`software.historyOp.${entry.operation}` as MessageKey)}
              </p>
              <p className="software-muted">
                <Actor userId={entry.actorUserId} system={entry.actorSystem} /> ·{' '}
                <TableDate value={entry.createdAt} />
              </p>
              {entry.reason ? (
                <p>{t(`software.reasonCode.${entry.reason}` as MessageKey)}</p>
              ) : null}
              <HashChip short value={entry.installerSha256} label={t('software.history.hash')} />
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

function VersionOperationDialog({
  version,
  action,
  onClose,
  onDone,
}: {
  version: SoftwareVersion;
  action: VersionAction['id'];
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const reasons =
    action === 'reject'
      ? versionReasons.reject
      : action === 'revoke'
        ? versionReasons.revoke
        : undefined;
  return (
    <OperationDialog
      title={t(`software.versionOp.${action}` as MessageKey)}
      description={
        <>
          <p>
            <strong>
              {t('software.version.title', {
                product: version.productName,
                version: version.productVersion,
              })}
            </strong>
          </p>
          <p>{t(`software.versionOpHint.${action}` as MessageKey)}</p>
          <HashChip value={version.installerSha256} label={t('software.binding.installerSha256')} />
        </>
      }
      confirmLabel={t(`software.versionOp.${action}` as MessageKey)}
      danger={action === 'reject' || action === 'revoke'}
      {...(reasons ? { reasons } : {})}
      onClose={onClose}
      onSubmit={async (reason) => {
        if (action === 'package') await softwareApi.package(version.id, version.version);
        else await softwareApi.versionOperation(version.id, action, version.version, reason);
        onDone();
      }}
    />
  );
}

export function PublishDialog({
  pkg,
  onClose,
  onDone,
}: {
  pkg: SoftwarePackage;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  return (
    <OperationDialog
      title={t('software.packageOp.publish')}
      description={<p>{t('software.packageOpHint.publish')}</p>}
      confirmLabel={t('software.packageOp.publish')}
      onClose={onClose}
      onSubmit={async () => {
        await softwareApi.publish(pkg.id, pkg.version);
        onDone();
      }}
    />
  );
}

const emptyForm = {
  version: '',
  installerSha256: '',
  installerUrl: '',
  publisher: '',
  installCommand: '',
  detectionRule: '',
};

export function SoftwareVersionRegisterScreen() {
  const { t } = useI18n();
  const [productId, setProductId] = useState(
    () => new URLSearchParams(window.location.search).get('productId') ?? '',
  );
  const [form, setForm] = useState(emptyForm);
  const [errors, setErrors] = useState<RegisterErrors>({});
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const products = usePagedList((cursor, signal) => softwareApi.products('', cursor, signal), []);
  const set = (key: keyof typeof emptyForm) => (value: string) =>
    setForm((previous) => ({ ...previous, [key]: value }));
  const fieldError = (key: keyof RegisterErrors) => {
    const message = errors[key];
    return message ? t(message) : undefined;
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const input = { productId, ...form };
    const found = validateRegister(input);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    setError(undefined);
    try {
      const created = await softwareApi.register({
        ...input,
        version: form.version.trim(),
        installerSha256: form.installerSha256.trim().toLowerCase(),
        installerUrl: form.installerUrl.trim(),
        publisher: form.publisher.trim(),
        installCommand: form.installCommand.trim(),
        detectionRule: form.detectionRule.trim(),
      });
      navigate(`/software/versions/${enc(created.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <div className="software-workspace">
      <PageHeader
        eyebrow={t('software.eyebrow')}
        title={t('software.register.title')}
        intro={t('software.register.intro')}
        actions={
          <Link className="btn btn-secondary" to="/software">
            {t('software.back')}
          </Link>
        }
      />
      <form className="software-card software-form" noValidate onSubmit={(e) => void submit(e)}>
        {products.error ? <ApiErrorAlert error={products.error} onRetry={products.reload} /> : null}
        <Select
          label={t('software.register.product')}
          value={productId}
          required
          error={fieldError('productId')}
          onChange={(event) => setProductId(event.target.value)}
          options={[
            {
              value: '',
              label: products.loading ? t('state.loading') : t('software.register.chooseProduct'),
            },
            ...products.items.map((product) => ({
              value: product.id,
              label: product.publisher ? `${product.name} (${product.publisher})` : product.name,
            })),
          ]}
        />
        {products.hasMore ? (
          <Button onClick={products.loadMore} busy={products.loadingMore}>
            {t('software.register.moreProducts')}
          </Button>
        ) : null}
        <TextField
          label={t('software.version.number')}
          value={form.version}
          maxLength={100}
          required
          error={fieldError('version')}
          onChange={(event) => set('version')(event.target.value)}
        />
        <TextField
          label={t('software.publisher')}
          hint={t('software.register.publisherHint')}
          value={form.publisher}
          maxLength={200}
          onChange={(event) => set('publisher')(event.target.value)}
        />
        <TextField
          className="software-mono-input"
          label={t('software.binding.installerSha256')}
          hint={t('software.register.sha256Hint')}
          value={form.installerSha256}
          maxLength={64}
          required
          spellCheck={false}
          autoComplete="off"
          error={fieldError('installerSha256')}
          onChange={(event) => set('installerSha256')(event.target.value)}
        />
        <TextField
          label={t('software.binding.installerUrl')}
          hint={t('software.register.urlHint')}
          type="url"
          value={form.installerUrl}
          maxLength={2000}
          required
          error={fieldError('installerUrl')}
          onChange={(event) => set('installerUrl')(event.target.value)}
        />
        <TextField
          className="software-mono-input"
          label={t('software.binding.installCommand')}
          hint={t('software.register.commandHint')}
          value={form.installCommand}
          maxLength={2000}
          required
          spellCheck={false}
          error={fieldError('installCommand')}
          onChange={(event) => set('installCommand')(event.target.value)}
        />
        <TextArea
          className="software-mono-input"
          label={t('software.binding.detectionRule')}
          hint={t('software.register.ruleHint')}
          value={form.detectionRule}
          maxLength={4000}
          rows={4}
          required
          spellCheck={false}
          error={fieldError('detectionRule')}
          onChange={(event) => set('detectionRule')(event.target.value)}
        />
        <Alert kind="info">{t('software.register.bindingNote')}</Alert>
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="software-form-actions">
          <Button type="submit" variant="primary" busy={busy}>
            {t('software.register.submit')}
          </Button>
        </div>
      </form>
    </div>
  );
}
