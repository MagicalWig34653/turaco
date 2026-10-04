import { useState, type FormEvent } from 'react';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Dialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { securityApi } from './api';
import { AdvisoryRemediation, FindingRemediation } from './Remediation';
import {
  advisoryActions,
  devicePath,
  findingActions,
  maxReviewDate,
  reviewStartDate,
  safeSourceUrl,
  validReviewDate,
  isApplicableAdvisory,
  isOpenFinding,
  riskReviewDueWithin30Days,
} from './helpers';
import {
  advisoryStatuses,
  confidences,
  falsePositiveReasons,
  findingStatuses,
  notApplicableReasons,
  reopenReasons,
  riskReasons,
  ruleKinds,
  severities,
  type Criterion,
  type Finding,
  type Transition,
} from './types';
const enc = encodeURIComponent;
function Label({ kind, value }: { kind: string; value: string }) {
  const { t } = useI18n();
  return (
    <Badge
      tone={
        value === 'critical' || value === 'high'
          ? 'danger'
          : value === 'potential'
            ? 'warning'
            : 'info'
      }
    >
      {t(`security.${kind}.${value}` as MessageKey)}
    </Badge>
  );
}
function UnmatchedCriteriaBadge({ count }: { count: number }) {
  const { t } = useI18n();
  return count > 0 ? (
    <Badge tone="warning">{t('security.criteriaNotEvaluated', { count })}</Badge>
  ) : null;
}
function Field({
  label,
  value,
  onChange,
  type = 'text',
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  type?: string;
}) {
  return (
    <label>
      {label}
      <input type={type} value={value} onChange={(e) => onChange(e.target.value)} />
    </label>
  );
}
function Transitions({ items }: { items: Transition[] }) {
  const { t } = useI18n();
  return (
    <section>
      <h2>{t('security.transitions')}</h2>
      <table>
        <thead>
          <tr>
            <th>{t('security.date')}</th>
            <th>{t('security.operation')}</th>
            <th>{t('security.status')}</th>
            <th>{t('security.reason')}</th>
          </tr>
        </thead>
        <tbody>
          {items.map((x) => (
            <tr key={x.id}>
              <td>{new Date(x.createdAt).toLocaleString()}</td>
              <td>{t(`security.operation.${x.operation}` as MessageKey)}</td>
              <td>
                <Label kind="status" value={x.toStatus} />
              </td>
              <td>{x.reason ? t(`security.reason.${x.reason}` as MessageKey) : '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}
function ImportDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { t } = useI18n();
  const [json, setJson] = useState('');
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(undefined);
    setBusy(true);
    try {
      if (new Blob([json]).size > 1024 * 1024) throw new Error(t('security.importLimit'));
      const parsed: unknown = JSON.parse(json);
      const records = Array.isArray(parsed)
        ? parsed
        : typeof parsed === 'object' && parsed !== null && 'records' in parsed
          ? (parsed as { records: unknown }).records
          : null;
      if (!Array.isArray(records) || records.length < 1 || records.length > 500)
        throw new Error(t('security.importLimit'));
      await securityApi.import(records);
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t('security.import')} onClose={onClose}>
      <form className="form-stack" onSubmit={(e) => void submit(e)}>
        <label>
          {t('security.importJson')}
          <textarea
            value={json}
            maxLength={1024 * 1024}
            onChange={(e) => setJson(e.target.value)}
            required
          />
        </label>
        <p>{t('security.importLimit')}</p>
        <p>{t('security.importIgnored')}</p>
        {error && <ApiErrorAlert error={error} />}
        <div className="actions">
          <button type="submit" disabled={busy}>
            {t('security.import')}
          </button>
          <button type="button" onClick={onClose}>
            {t('action.cancel')}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
export function AdvisoriesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState(
    () => new URLSearchParams(window.location.search).get('status') ?? '',
  );
  const [severity, setSeverity] = useState(
    () => new URLSearchParams(window.location.search).get('severity') ?? '',
  );
  const [q, setQ] = useState('');
  const [importOpen, setImportOpen] = useState(false);
  const list = usePagedList(
    (cursor, signal) => securityApi.advisories({ status, severity, q }, cursor, signal),
    [status, severity, q],
  );
  const visibleAdvisories = list.items.filter(
    (x) =>
      !new URLSearchParams(window.location.search).has('applicable') ||
      isApplicableAdvisory(x.status),
  );
  return (
    <>
      <PageHeader
        title={t('security.advisories')}
        actions={
          can('security.manage') ? (
            <>
              <Link to="/security/advisories/new">{t('security.create')}</Link>{' '}
              <button onClick={() => setImportOpen(true)}>{t('security.import')}</button>
            </>
          ) : undefined
        }
      />
      <div className="filters">
        <label>
          {t('security.status')}
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">{t('filters.all')}</option>
            {advisoryStatuses.map((x) => (
              <option key={x} value={x}>
                {t(`security.status.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('security.severity')}
          <select value={severity} onChange={(e) => setSeverity(e.target.value)}>
            <option value="">{t('filters.all')}</option>
            {severities.map((x) => (
              <option key={x} value={x}>
                {t(`security.severity.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        <Field label={t('security.search')} value={q} onChange={setQ} type="search" />
      </div>
      <p>{t('security.loadedCount', { count: visibleAdvisories.length })}</p>
      {list.error && <ApiErrorAlert error={list.error} onRetry={list.reload} />}
      <table>
        <thead>
          <tr>
            <th>{t('security.reference')}</th>
            <th>{t('security.title')}</th>
            <th>{t('security.severity')}</th>
            <th>{t('security.status')}</th>
            <th>{t('security.source')}</th>
          </tr>
        </thead>
        <tbody>
          {visibleAdvisories.map((x) => (
            <tr key={x.id}>
              <td>
                <Link to={`/security/advisories/${enc(x.id)}`}>{x.reference}</Link>
              </td>
              <td>
                {x.title} <UnmatchedCriteriaBadge count={x.unmatchedCriteria} />
              </td>
              <td>
                <Label kind="severity" value={x.severity} />
              </td>
              <td>
                <Label kind="status" value={x.status} />
              </td>
              <td>{x.source}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {list.hasMore && <button onClick={list.loadMore}>{t('action.loadMore')}</button>}
      {importOpen && (
        <ImportDialog
          onClose={() => setImportOpen(false)}
          onDone={() => {
            setImportOpen(false);
            list.reload();
          }}
        />
      )}
    </>
  );
}
function CriteriaEditor({
  criteria,
  version,
  id,
  onDone,
  onClose,
}: {
  criteria: Criterion[];
  version: number;
  id: string;
  onDone: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const [rows, setRows] = useState<Criterion[]>(
    criteria.map((x) => ({ ...x, rules: x.rules.map((r) => ({ ...r })) })),
  );
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  function edit(i: number, patch: Partial<Criterion>) {
    setRows((old) => old.map((x, n) => (n === i ? { ...x, ...patch } : x)));
  }
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await securityApi.criteria(
        id,
        version,
        rows.map(({ softwareProductId, productName, publisher, osPlatform, rules }) => ({
          softwareProductId,
          productName,
          publisher,
          osPlatform,
          rules,
        })),
      );
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog title={t('security.editCriteria')} onClose={onClose} wide>
      <form className="form-stack" onSubmit={(e) => void save(e)}>
        {rows.map((x, i) => (
          <fieldset key={i}>
            <legend>
              {t('security.criterion')} {i + 1}
            </legend>
            <Field
              label={t('security.softwareProductId')}
              value={x.softwareProductId ?? ''}
              onChange={(v) => edit(i, { softwareProductId: v })}
            />
            <Field
              label={t('security.productName')}
              value={x.productName ?? ''}
              onChange={(v) => edit(i, { productName: v })}
            />
            <Field
              label={t('security.publisher')}
              value={x.publisher ?? ''}
              onChange={(v) => edit(i, { publisher: v })}
            />
            <Field
              label={t('security.osPlatform')}
              value={x.osPlatform ?? ''}
              onChange={(v) => edit(i, { osPlatform: v })}
            />
            {x.rules.map((rule, j) => (
              <div className="filters" key={j}>
                <label>
                  {t('security.ruleKind')}
                  <select
                    value={rule.kind}
                    onChange={(e) =>
                      edit(i, {
                        rules: x.rules.map((r, n) =>
                          n === j ? { ...r, kind: e.target.value } : r,
                        ),
                      })
                    }
                  >
                    {ruleKinds.map((k) => (
                      <option key={k} value={k}>
                        {t(`security.rule.${k}` as MessageKey)}
                      </option>
                    ))}
                  </select>
                </label>
                <Field
                  label={t('security.version')}
                  value={rule.version}
                  onChange={(v) =>
                    edit(i, { rules: x.rules.map((r, n) => (n === j ? { ...r, version: v } : r)) })
                  }
                />
                <button
                  type="button"
                  onClick={() => edit(i, { rules: x.rules.filter((_, n) => n !== j) })}
                >
                  {t('security.remove')}
                </button>
              </div>
            ))}
            <button
              type="button"
              onClick={() => edit(i, { rules: [...x.rules, { kind: 'eq', version: '' }] })}
            >
              {t('security.addRule')}
            </button>
            <button type="button" onClick={() => setRows((old) => old.filter((_, n) => n !== i))}>
              {t('security.remove')}
            </button>
          </fieldset>
        ))}
        <button
          type="button"
          onClick={() =>
            setRows((old) => [
              ...old,
              { softwareProductId: '', productName: '', publisher: '', osPlatform: '', rules: [] },
            ])
          }
        >
          {t('security.addCriterion')}
        </button>
        {error && <ApiErrorAlert error={error} />}
        <div className="actions">
          <button type="submit" disabled={busy}>
            {t('action.save')}
          </button>
          <button type="button" onClick={onClose}>
            {t('action.cancel')}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
export function AdvisoryCreateScreen() {
  const { t } = useI18n();
  const [title, setTitle] = useState('');
  const [summary, setSummary] = useState('');
  const [severity, setSeverity] = useState('medium');
  const [sourceUrl, setSourceUrl] = useState('');
  const [error, setError] = useState<ApiError>();
  async function save(e: FormEvent) {
    e.preventDefault();
    try {
      const data = await securityApi.create({ title, summary, severity, sourceUrl, criteria: [] });
      navigate(`/security/advisories/${enc(data.advisory.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  return (
    <>
      <PageHeader title={t('security.create')} />
      <form className="form-stack" onSubmit={(e) => void save(e)}>
        <Field label={t('security.title')} value={title} onChange={setTitle} />
        <label>
          {t('security.summary')}
          <textarea value={summary} onChange={(e) => setSummary(e.target.value)} />
        </label>
        <label>
          {t('security.severity')}
          <select value={severity} onChange={(e) => setSeverity(e.target.value)}>
            {severities.map((x) => (
              <option key={x} value={x}>
                {t(`security.severity.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        <Field
          label={t('security.sourceUrl')}
          value={sourceUrl}
          onChange={setSourceUrl}
          type="url"
        />
        {error && <ApiErrorAlert error={error} />}
        <button type="submit">{t('action.save')}</button>
      </form>
    </>
  );
}
export function AdvisoryDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const detail = useAsync((signal) => securityApi.advisory(id, signal), [id]);
  const summary = useAsync(
    (signal) => securityApi.summary(id, signal),
    [id, detail.data?.advisory.version],
  );
  const findings = usePagedList(
    (cursor, signal) => securityApi.advisoryFindings(id, cursor, signal),
    [id, detail.data?.advisory.version],
  );
  const transitions = useAsync(
    (signal) => securityApi.advisoryTransitions(id, signal),
    [id, detail.data?.advisory.version],
  );
  const [edit, setEdit] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [warnings, setWarnings] = useState<string[]>([]);
  const [reasonAction, setReasonAction] = useState('');
  const [reason, setReason] = useState('');
  const a = detail.data?.advisory;
  async function action(op: string, reasonValue?: string) {
    if (!a) return;
    try {
      const result = await securityApi.advisoryAction(id, op, a.version, reasonValue);
      setWarnings(result.warnings ?? []);
      setError(undefined);
      setReasonAction('');
      detail.reload();
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  async function normalize() {
    if (!a) return;
    try {
      await securityApi.normalize(id, a.version);
      detail.reload();
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  return (
    <>
      <PageHeader title={a?.title ?? t('security.advisory')} />
      {detail.error && <ApiErrorAlert error={detail.error} onRetry={detail.reload} />}
      {a && (
        <>
          <p>
            {a.reference} · <Label kind="severity" value={a.severity} /> ·{' '}
            <Label kind="status" value={a.status} />{' '}
            <UnmatchedCriteriaBadge count={a.unmatchedCriteria} />
          </p>
          <section>
            <h2>{t('security.facts')}</h2>
            <p>{a.summary}</p>
            <dl>
              <dt>{t('security.source')}</dt>
              <dd>
                {a.source} {a.externalId}
              </dd>
              <dt>{t('security.sourceUrl')}</dt>
              <dd>
                {safeSourceUrl(a.sourceUrl) ? (
                  <a
                    href={safeSourceUrl(a.sourceUrl) ?? undefined}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {a.sourceUrl}
                  </a>
                ) : (
                  a.sourceUrl
                )}
              </dd>
              <dt>{t('security.publishedAt')}</dt>
              <dd>{a.publishedAt ?? '—'}</dd>
              <dt>{t('security.modifiedAt')}</dt>
              <dd>{a.modifiedAt ?? '—'}</dd>
              <dt>{t('security.matchedAt')}</dt>
              <dd>{a.matchedAt ?? '—'}</dd>
            </dl>
            {a.matchTruncated && <p>{t('security.truncated')}</p>}
          </section>
          <section>
            <h2>{t('security.criteria')}</h2>
            {can('security.manage') && (
              <>
                <button onClick={() => setEdit(true)}>{t('security.editCriteria')}</button>
                <button onClick={() => void normalize()}>{t('security.normalize')}</button>
              </>
            )}
            <table>
              <thead>
                <tr>
                  <th>{t('security.productName')}</th>
                  <th>{t('security.publisher')}</th>
                  <th>{t('security.osPlatform')}</th>
                  <th>{t('security.rules')}</th>
                  <th>{t('security.normalization')}</th>
                </tr>
              </thead>
              <tbody>
                {detail.data?.criteria.map((c) => (
                  <tr key={c.id}>
                    <td>
                      {c.productName ||
                        detail.data?.productNames?.[c.softwareProductId ?? ''] ||
                        c.softwareProductId}
                    </td>
                    <td>{c.publisher}</td>
                    <td>{c.osPlatform}</td>
                    <td>
                      {c.rules
                        .map((r) => `${t(`security.rule.${r.kind}` as MessageKey)} ${r.version}`)
                        .join(', ')}
                    </td>
                    <td>{t(`security.normalization.${c.normalization}` as MessageKey)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>
          {can('security.manage') && (
            <section>
              <h2>{t('security.actions')}</h2>
              <div className="actions">
                {advisoryActions(a.status).map((op) => (
                  <button
                    key={op}
                    onClick={() =>
                      op === 'not-applicable' ? setReasonAction(op) : void action(op)
                    }
                  >
                    {t(`security.action.${op}` as MessageKey)}
                  </button>
                ))}
              </div>
            </section>
          )}
          {warnings.includes('unmatched_criteria') && (
            <p role="status">{t('security.actionWarningUnmatched')}</p>
          )}
          {summary.data && (
            <section>
              <h2>{t('security.summaryCounts')}</h2>
              <p>
                {t('security.affectedDevices')}: {summary.data.affectedDevices}
              </p>
              <p>
                <UnmatchedCriteriaBadge count={summary.data.unmatchedCriteria} />
              </p>
              <p>
                {t('security.oldestOpen')}: {summary.data.oldestOpenSince ?? '—'}
              </p>
              {Object.entries(summary.data.byStatus ?? {}).map(([k, v]) => (
                <p key={k}>
                  <Label kind="status" value={k} /> {v}
                </p>
              ))}
              {Object.entries(summary.data.byConfidence ?? {}).map(([k, v]) => (
                <p key={k}>
                  <Label kind="confidence" value={k} /> {v}
                </p>
              ))}
            </section>
          )}
          <AdvisoryRemediation id={id} version={a.version} onChanged={detail.reload} />
          <section>
            <h2>{t('security.affectedDevices')}</h2>
            <FindingTable items={findings.items} />
            {findings.hasMore && (
              <button onClick={findings.loadMore}>{t('action.loadMore')}</button>
            )}
          </section>
          {transitions.data && <Transitions items={transitions.data.items} />}
        </>
      )}
      {error && <ApiErrorAlert error={error} />}
      {edit && a && (
        <CriteriaEditor
          criteria={detail.data?.criteria ?? []}
          version={a.version}
          id={id}
          onClose={() => setEdit(false)}
          onDone={() => {
            setEdit(false);
            detail.reload();
          }}
        />
      )}
      {reasonAction && (
        <ReasonDialog
          title={t(`security.action.${reasonAction}` as MessageKey)}
          reasons={
            can('security.accept_risk')
              ? notApplicableReasons
              : notApplicableReasons.filter((x) => x !== 'other')
          }
          onClose={() => setReasonAction('')}
          onSubmit={(value) => void action(reasonAction, value)}
          value={reason}
          setValue={setReason}
        />
      )}
    </>
  );
}
function FindingTable({ items }: { items: Finding[] }) {
  const { t } = useI18n();
  const { can } = useSession();
  return (
    <table>
      <thead>
        <tr>
          <th>{t('security.reference')}</th>
          <th>{t('security.advisory')}</th>
          <th>{t('security.device')}</th>
          <th>{t('security.productName')}</th>
          <th>{t('security.installedVersion')}</th>
          <th>{t('security.confidence')}</th>
          <th>{t('security.status')}</th>
        </tr>
      </thead>
      <tbody>
        {items.map((x) => (
          <tr key={x.id}>
            <td>
              <Link to={`/security/findings/${enc(x.id)}`}>{x.reference}</Link>
            </td>
            <td>
              <Link to={`/security/advisories/${enc(x.advisoryId)}`}>{x.advisoryReference}</Link>
            </td>
            <td>
              {devicePath(x, can('endpoints.view') || can('endpoints.manage')) ? (
                <Link to={devicePath(x, true) ?? ''}>{x.deviceName}</Link>
              ) : (
                t('security.deviceHidden')
              )}
            </td>
            <td>{x.productName ?? x.softwareProductId}</td>
            <td>{x.installedVersion ?? '—'}</td>
            <td>
              <span title={t('security.confidenceHelp')}>
                <Label kind="confidence" value={x.confidence} />
              </span>
            </td>
            <td>
              <Label kind="status" value={x.status} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
export function SecurityFindingsScreen() {
  const { t } = useI18n();
  const query = new URLSearchParams(window.location.search);
  const [status, setStatus] = useState(() =>
    query.get('riskDue') === 'true' ? 'risk_accepted' : '',
  );
  const [confidence, setConfidence] = useState(() => query.get('confidence') ?? '');
  const [advisoryId, setAdvisoryId] = useState('');
  const [device, setDevice] = useState('');
  const list = usePagedList(
    (cursor, signal) => securityApi.findings({ status, confidence, advisoryId }, cursor, signal),
    [status, confidence, advisoryId],
  );
  const visible = list.items.filter(
    (x) =>
      (!query.has('open') || isOpenFinding(x.status)) &&
      (!query.has('riskDue') || riskReviewDueWithin30Days(x, new Date())) &&
      (!device ||
        x.deviceName?.toLowerCase().includes(device.toLowerCase()) ||
        x.deviceId === device),
  );
  return (
    <>
      <PageHeader title={t('security.findings')} />
      <p>{t('security.confidenceHelp')}</p>
      <div className="filters">
        <label>
          {t('security.status')}
          <select value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">{t('filters.all')}</option>
            {findingStatuses.map((x) => (
              <option key={x} value={x}>
                {t(`security.status.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('security.confidence')}
          <select value={confidence} onChange={(e) => setConfidence(e.target.value)}>
            <option value="">{t('filters.all')}</option>
            {confidences.map((x) => (
              <option key={x} value={x}>
                {t(`security.confidence.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        <Field label={t('security.advisoryId')} value={advisoryId} onChange={setAdvisoryId} />
        <Field label={t('security.device')} value={device} onChange={setDevice} type="search" />
      </div>
      <p>
        {t('security.loadedCount', {
          count: visible.length,
        })}
      </p>
      {list.error && <ApiErrorAlert error={list.error} onRetry={list.reload} />}
      <FindingTable items={visible} />
      {list.hasMore && <button onClick={list.loadMore}>{t('action.loadMore')}</button>}
    </>
  );
}
function ReasonDialog({
  title,
  reasons,
  onClose,
  onSubmit,
  value,
  setValue,
  reviewBy,
  setReviewBy,
}: {
  title: string;
  reasons: readonly string[];
  onClose: () => void;
  onSubmit: (reason: string, reviewBy?: string) => void;
  value: string;
  setValue: (v: string) => void;
  reviewBy?: string;
  setReviewBy?: ((v: string) => void) | undefined;
}) {
  const { t } = useI18n();
  const today = new Date();
  return (
    <Dialog title={title} onClose={onClose}>
      <form
        className="form-stack"
        onSubmit={(e) => {
          e.preventDefault();
          if (value && (!setReviewBy || validReviewDate(reviewBy ?? '', today)))
            onSubmit(value, reviewBy);
        }}
      >
        <label>
          {t('security.reason')}
          <select value={value} required onChange={(e) => setValue(e.target.value)}>
            <option value="">{t('security.selectReason')}</option>
            {reasons.map((x) => (
              <option key={x} value={x}>
                {t(`security.reason.${x}` as MessageKey)}
              </option>
            ))}
          </select>
        </label>
        {setReviewBy && (
          <label>
            {t('security.reviewBy')}
            <input
              type="date"
              required
              min={reviewStartDate(today)}
              max={maxReviewDate(today)}
              value={reviewBy ?? ''}
              onChange={(e) => setReviewBy(e.target.value)}
            />
          </label>
        )}
        <div className="actions">
          <button type="submit">{t('action.save')}</button>
          <button type="button" onClick={onClose}>
            {t('action.cancel')}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
export function SecurityFindingDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const detail = useAsync((signal) => securityApi.finding(id, signal), [id]);
  const transitions = useAsync(
    (signal) => securityApi.findingTransitions(id, signal),
    [id, detail.data?.version],
  );
  const [dialog, setDialog] = useState('');
  const [reason, setReason] = useState('');
  const [reviewBy, setReviewBy] = useState('');
  const [error, setError] = useState<ApiError>();
  const f = detail.data;
  async function action(op: string, why?: string, date?: string) {
    if (!f) return;
    try {
      await securityApi.findingAction(id, op, {
        expectedVersion: f.version,
        ...(why ? { reason: why } : {}),
        ...(date ? { reviewBy: date } : {}),
      });
      setDialog('');
      detail.reload();
    } catch (cause) {
      setError(asApiError(cause));
    }
  }
  const reasons =
    dialog === 'accept-risk'
      ? riskReasons
      : dialog === 'false-positive'
        ? can('security.accept_risk')
          ? falsePositiveReasons
          : falsePositiveReasons.filter((x) => x !== 'other' && x !== 'configuration_not_affected')
        : reopenReasons;
  return (
    <>
      <PageHeader title={f?.reference ?? t('security.finding')} />
      {detail.error && <ApiErrorAlert error={detail.error} onRetry={detail.reload} />}
      {f && (
        <>
          <p>
            <Label kind="status" value={f.status} />{' '}
            <span title={t('security.confidenceHelp')}>
              <Label kind="confidence" value={f.confidence} />
            </span>
          </p>
          <p>{t('security.confidenceHelp')}</p>
          <dl>
            <dt>{t('security.advisory')}</dt>
            <dd>
              <Link to={`/security/advisories/${enc(f.advisoryId)}`}>
                {f.advisoryReference} {f.advisoryTitle}
              </Link>
            </dd>
            <dt>{t('security.device')}</dt>
            <dd>
              {devicePath(f, can('endpoints.view') || can('endpoints.manage')) ? (
                <Link to={devicePath(f, true) ?? ''}>{f.deviceName}</Link>
              ) : (
                t('security.deviceHidden')
              )}
            </dd>
            <dt>{t('security.productName')}</dt>
            <dd>{f.productName ?? f.softwareProductId}</dd>
            <dt>{t('security.installedVersion')}</dt>
            <dd>{f.installedVersion ?? '—'}</dd>
            <dt>{t('security.firstSeen')}</dt>
            <dd>{f.firstSeenAt}</dd>
            <dt>{t('security.lastSeen')}</dt>
            <dd>{f.lastSeenAt}</dd>
            <dt>{t('security.reviewBy')}</dt>
            <dd>{f.riskReviewBy ?? '—'}</dd>
          </dl>
          <FindingRemediation id={id} version={f.version} onCreated={detail.reload} />
          <section>
            <h2>{t('security.actions')}</h2>
            <div className="actions">
              {findingActions(f.status, can('security.manage'), can('security.accept_risk')).map(
                (op) => (
                  <button
                    key={op}
                    onClick={() =>
                      ['accept-risk', 'false-positive', 'reopen'].includes(op)
                        ? setDialog(op)
                        : void action(op)
                    }
                  >
                    {t(`security.action.${op}` as MessageKey)}
                  </button>
                ),
              )}
            </div>
          </section>
          {transitions.data && <Transitions items={transitions.data.items} />}
        </>
      )}
      {error && <ApiErrorAlert error={error} />}
      {dialog && (
        <ReasonDialog
          title={t(`security.action.${dialog}` as MessageKey)}
          reasons={reasons}
          onClose={() => setDialog('')}
          onSubmit={(why, date) => void action(dialog, why, date)}
          value={reason}
          setValue={setReason}
          reviewBy={reviewBy}
          setReviewBy={dialog === 'accept-risk' ? setReviewBy : undefined}
        />
      )}
    </>
  );
}
