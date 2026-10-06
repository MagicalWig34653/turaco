import { useId, useState, type FormEvent, type ReactNode } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { EmptyState, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { changesApi } from '../changes/api';
import { endpointsApi } from '../endpoints/api';
import { organizationApi } from '../organization/api';
import { softwareApi } from '../software/api';
import { targetSetsApi } from './api';
import { approvalTone, breakdown, codeKey, deploymentTone, isUuid, windowState } from './helpers';
import type { ChangeWindow, PlanIssue, TargetEvaluation } from './types';

const enc = encodeURIComponent;

export function DeploymentStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return (
    <StatusBadge tone={deploymentTone(status)} live={status === 'pending_approval'}>
      {t(codeKey('deployments.status', status, 'deployments.unknownValue'))}
    </StatusBadge>
  );
}

export function HighImpactBadge() {
  const { t } = useI18n();
  return <StatusBadge tone="warning">{t('deployments.highImpact')}</StatusBadge>;
}

export function ApprovalBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return (
    <StatusBadge tone={approvalTone(status)}>
      {t(codeKey('deployments.approvalStatus', status, 'deployments.unknownValue'))}
    </StatusBadge>
  );
}

/** Localized text for a validation issue (count and other plan are shown when present). */
export function IssueText({
  issue,
  ringName,
}: {
  issue: PlanIssue;
  ringName?: string | undefined;
}) {
  const { t } = useI18n();
  return (
    <>
      {t(codeKey('deployments.issue', issue.code, 'deployments.issue.unknown'), {
        count: issue.count ?? 0,
        code: issue.code,
      })}
      {ringName ? (
        <span className="deployments-muted">
          {' '}
          · {t('deployments.issue.inRing', { ring: ringName })}
        </span>
      ) : null}
      {issue.deploymentId ? (
        <>
          {' '}
          <Link to={`/deployments/${enc(issue.deploymentId)}`}>
            {t('deployments.issue.openOther')}
          </Link>
        </>
      ) : null}
    </>
  );
}

export type PickItem = { id: string; label: string; detail?: string | undefined };

/**
 * Search-and-add picker. Without the read permission it falls back to entering an id, so a planner
 * is never blocked by a missing lookup permission (the API validates the id).
 */
export function SearchPicker({
  label,
  hint,
  allowed,
  search,
  onPick,
  manualPattern = 'uuid',
}: {
  label: string;
  hint?: string | undefined;
  allowed: boolean;
  search: (q: string, signal: AbortSignal) => Promise<PickItem[]>;
  onPick: (item: PickItem) => void;
  manualPattern?: 'uuid' | 'text';
}) {
  const { t } = useI18n();
  const listId = useId();
  const [query, setQuery] = useState('');
  const debounced = useDebouncedValue(query.trim(), 250);
  const results = useAsync(
    (signal) =>
      allowed && debounced.length >= 2 ? search(debounced, signal) : Promise.resolve([]),
    [allowed, debounced],
  );
  const manualValid = manualPattern === 'uuid' ? isUuid(query) : query.trim().length > 0;
  const addManual = (event?: FormEvent) => {
    event?.preventDefault();
    if (!manualValid) return;
    onPick({ id: query.trim(), label: query.trim() });
    setQuery('');
  };
  return (
    <div className="deployments-picker">
      <div className="deployments-picker-row">
        <TextField
          label={label}
          hint={
            hint ??
            (allowed
              ? t('deployments.picker.searchHint')
              : manualPattern === 'uuid'
                ? t('deployments.picker.idHint')
                : t('deployments.picker.textHint'))
          }
          type={allowed ? 'search' : 'text'}
          value={query}
          maxLength={200}
          autoComplete="off"
          aria-controls={allowed ? listId : undefined}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              if (manualValid) addManual();
            }
          }}
        />
        <Button disabled={!manualValid} onClick={() => addManual()}>
          {t('deployments.picker.add')}
        </Button>
      </div>
      {allowed ? (
        <div id={listId} aria-busy={results.loading} aria-live="polite">
          {results.error ? <ApiErrorAlert error={results.error} onRetry={results.reload} /> : null}
          {debounced.length >= 2 && !results.loading && results.data?.length === 0 ? (
            <p className="deployments-muted">{t('deployments.picker.noResults')}</p>
          ) : null}
          {results.data?.length ? (
            <ul className="deployments-picker-results">
              {results.data.map((item) => (
                <li key={item.id}>
                  <button
                    type="button"
                    className="deployments-picker-option"
                    onClick={() => {
                      onPick(item);
                      setQuery('');
                    }}
                  >
                    <span>{item.label}</span>
                    {item.detail ? <small>{item.detail}</small> : null}
                  </button>
                </li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

export const canViewDevices = (can: (permission: string) => boolean) =>
  can('endpoints.view') || can('endpoints.manage');

export function DevicePicker({
  label,
  onPick,
}: {
  label: string;
  onPick: (item: PickItem) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  return (
    <SearchPicker
      label={label}
      allowed={canViewDevices(can)}
      onPick={onPick}
      search={async (q, signal) => {
        const page = await endpointsApi.devices(
          {
            platform: '',
            compliance: '',
            q,
            linked: '',
            includeDeleted: false,
            managementState: '',
            hasFinding: '',
            osVersion: '',
            lastCheckinOlderThanDays: '',
          },
          undefined,
          signal,
        );
        return page.items.slice(0, 10).map((device) => ({
          id: device.id,
          label: device.name,
          detail: [
            t(codeKey('deployments.platform', device.osPlatform, 'deployments.unknownValue')),
            device.model,
            device.serialNumber,
          ]
            .filter(Boolean)
            .join(' · '),
        }));
      }}
    />
  );
}

/** Chips for picked ids; names are known for ids picked in this session. */
export function IdChips({
  ids,
  names,
  onRemove,
  linkDevices,
  label,
}: {
  ids: readonly string[];
  names: Readonly<Record<string, string>>;
  onRemove?: ((id: string) => void) | undefined;
  linkDevices?: boolean;
  label: string;
}) {
  const { t } = useI18n();
  if (ids.length === 0) return <p className="deployments-muted">{t('deployments.def.none')}</p>;
  return (
    <ul className="deployments-chips" aria-label={label}>
      {ids.map((id) => {
        const name = names[id] ?? `${id.slice(0, 8)}…`;
        return (
          <li key={id} className="deployments-chip">
            {linkDevices ? (
              <Link to={`/devices/${enc(id)}`} title={id}>
                {name}
              </Link>
            ) : (
              <span title={id}>{name}</span>
            )}
            {onRemove ? (
              <button
                type="button"
                aria-label={t('deployments.chip.remove', { name })}
                onClick={() => onRemove(id)}
              >
                ×
              </button>
            ) : null}
          </li>
        );
      })}
    </ul>
  );
}

export function LocationPicker({ onPick }: { onPick: (item: PickItem) => void }) {
  const { t } = useI18n();
  const { can } = useSession();
  return (
    <SearchPicker
      label={t('deployments.def.location.search')}
      allowed={can('organization.view')}
      onPick={onPick}
      search={async (q, signal) =>
        (await organizationApi.searchLocations(q, signal)).items
          .filter((location) => location.active)
          .map((location) => ({ id: location.id, label: location.name }))
      }
    />
  );
}

export function GroupPicker({ onPick }: { onPick: (item: PickItem) => void }) {
  const { t } = useI18n();
  const { can } = useSession();
  return (
    <SearchPicker
      label={t('deployments.def.group.search')}
      allowed={can('organization.directory.view')}
      manualPattern="text"
      onPick={onPick}
      search={async (q, signal) =>
        (await organizationApi.searchDirectoryGroups(q, signal)).items
          .filter((group) => !group.deletedObservedAt)
          .map((group) => ({
            id: group.externalId,
            label: group.displayName,
            detail: group.externalId,
          }))
      }
    />
  );
}

/** Change window period with its state relative to now. */
export function WindowPeriod({ change }: { change: ChangeWindow | undefined }) {
  const { t, locale } = useI18n();
  if (!change) return <>{t('deployments.window.unknown')}</>;
  const state = windowState(change, new Date());
  return (
    <span className="deployments-window">
      {change.windowStart ? (
        <time dateTime={change.windowStart}>{formatDateTime(locale, change.windowStart)}</time>
      ) : (
        '—'
      )}
      {' – '}
      {change.windowEnd ? (
        <time dateTime={change.windowEnd}>{formatDateTime(locale, change.windowEnd)}</time>
      ) : (
        '—'
      )}{' '}
      <StatusBadge
        tone={
          state === 'open'
            ? 'success'
            : state === 'ended'
              ? 'danger'
              : state === 'upcoming'
                ? 'info'
                : 'unknown'
        }
      >
        {t(`deployments.window.${state}` as MessageKey)}
      </StatusBadge>
    </span>
  );
}

export const canViewChanges = (can: (permission: string) => boolean) =>
  can('changes.view') || can('changes.manage') || can('changes.execute');

/**
 * Picks an approved or scheduled Change whose window has not ended. Without a Changes read
 * permission the Change id is entered directly.
 */
export function ChangePicker({
  value,
  onChange,
  error,
  known,
}: {
  value: string;
  onChange: (id: string) => void;
  error?: string | undefined;
  known?: ChangeWindow | undefined;
}) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const allowed = canViewChanges(can);
  const options = useAsync(
    async (signal) => {
      if (!allowed) return [];
      const [approved, scheduled] = await Promise.all(
        ['approved', 'scheduled'].map((status) => changesApi.list({ status }, undefined, signal)),
      );
      const now = Date.now();
      return [...(approved?.items ?? []), ...(scheduled?.items ?? [])].filter(
        (change) => !change.windowEnd || Date.parse(change.windowEnd) > now,
      );
    },
    [allowed],
  );
  if (!allowed)
    return (
      <TextField
        label={t('deployments.ring.change')}
        hint={t('deployments.ring.changeIdHint')}
        value={value}
        error={error}
        onChange={(event) => onChange(event.target.value.trim())}
      />
    );
  const items = options.data ?? [];
  const extra =
    value && !items.some((change) => change.id === value)
      ? [
          {
            value,
            label: known?.reference
              ? `${known.reference} (${t('deployments.window.current')})`
              : t('deployments.window.current'),
          },
        ]
      : [];
  return (
    <>
      <Select
        label={t('deployments.ring.change')}
        hint={t('deployments.ring.changeHint')}
        value={value}
        error={error}
        disabled={options.loading}
        onChange={(event) => onChange(event.target.value)}
        options={[
          {
            value: '',
            label: options.loading ? t('state.loading') : t('deployments.ring.changeChoose'),
          },
          ...extra,
          ...items.map((change) => ({
            value: change.id,
            label: `${change.reference} · ${change.title} · ${formatDateTime(locale, change.windowStart)} – ${formatDateTime(locale, change.windowEnd)}`,
          })),
        ]}
      />
      {options.error ? <ApiErrorAlert error={options.error} onRetry={options.reload} /> : null}
    </>
  );
}

export const canViewSoftware = (can: (permission: string) => boolean) =>
  can('software.view') || can('software.approve') || can('software.package');

/** Approved Software Versions; without a software read permission the version id is entered. */
export function VersionSelect({
  value,
  onChange,
  error,
  currentLabel,
}: {
  value: string;
  onChange: (id: string, label: string) => void;
  error?: string | undefined;
  currentLabel?: string | undefined;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const allowed = canViewSoftware(can);
  const versions = useAsync(
    (signal) =>
      allowed
        ? softwareApi.versions({ status: 'approved' }, undefined, signal).then((page) => page.items)
        : Promise.resolve([]),
    [allowed],
  );
  if (!allowed)
    return (
      <TextField
        label={t('deployments.field.version')}
        hint={t('deployments.field.versionIdHint')}
        value={value}
        error={error}
        required
        onChange={(event) => onChange(event.target.value.trim(), event.target.value.trim())}
      />
    );
  const items = versions.data ?? [];
  const extra =
    value && !items.some((version) => version.id === value)
      ? [{ value, label: currentLabel ?? value }]
      : [];
  return (
    <>
      <Select
        label={t('deployments.field.version')}
        hint={t('deployments.field.versionHint')}
        value={value}
        error={error}
        required
        disabled={versions.loading}
        onChange={(event) => {
          const picked = items.find((version) => version.id === event.target.value);
          onChange(
            event.target.value,
            picked ? `${picked.productName} ${picked.productVersion}` : event.target.value,
          );
        }}
        options={[
          {
            value: '',
            label: versions.loading ? t('state.loading') : t('deployments.field.versionChoose'),
          },
          ...extra,
          ...items.map((version) => ({
            value: version.id,
            label: `${version.productName} ${version.productVersion}`,
          })),
        ]}
      />
      {versions.error ? <ApiErrorAlert error={versions.error} onRetry={versions.reload} /> : null}
    </>
  );
}

function Bars({
  title,
  entries,
  prefix,
}: {
  title: string;
  entries: [string, number][];
  prefix: string;
}) {
  const { t } = useI18n();
  const max = Math.max(1, ...entries.map(([, n]) => n));
  return (
    <div className="deployments-bars">
      <h3>{title}</h3>
      {entries.length === 0 ? (
        <p className="deployments-muted">—</p>
      ) : (
        <dl>
          {entries.map(([key, count]) => (
            <div key={key}>
              <dt>{t(codeKey(prefix, key, 'deployments.unknownValue'))}</dt>
              <dd>
                <span className="deployments-bar" aria-hidden="true">
                  <span style={{ width: `${Math.round((count / max) * 100)}%` }} />
                </span>
                <span className="deployments-count">{count}</span>
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}

/** The live preview of a saved Target Set: counts, breakdowns, bounded examples and notices. */
export function EvaluationCard({
  targetSetId,
  dirty,
  onExplain,
}: {
  targetSetId: string;
  dirty: boolean;
  onExplain: (deviceId?: string) => void;
}) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const headingId = useId();
  const [token, setToken] = useState(0);
  const evaluation = useAsync(
    (signal) => targetSetsApi.evaluate(targetSetId, signal),
    [targetSetId, token],
  );
  const ev: TargetEvaluation | undefined = evaluation.data;
  const linkDevices = canViewDevices(can);
  return (
    <section className="deployments-card deployments-preview" aria-labelledby={headingId}>
      <div className="deployments-card-heading">
        <h2 id={headingId}>{t('deployments.eval.title')}</h2>
        <Button busy={evaluation.loading} onClick={() => setToken((n) => n + 1)}>
          {t('deployments.eval.refresh')}
        </Button>
      </div>
      {dirty ? <Alert kind="info">{t('deployments.eval.dirty')}</Alert> : null}
      {evaluation.error ? (
        <ApiErrorAlert error={evaluation.error} onRetry={evaluation.reload} />
      ) : null}
      {evaluation.loading && !ev ? <Skeleton lines={4} /> : null}
      {ev ? (
        <div aria-live="polite">
          <p className="deployments-big-number">
            <strong>{ev.matched.toLocaleString(locale)}</strong>
            {ev.truncated ? '+' : ''}{' '}
            <span>
              {t('deployments.eval.matched', { scanned: ev.scanned.toLocaleString(locale) })}
            </span>
          </p>
          {ev.truncated ? (
            <Alert kind="warning">{t('deployments.eval.truncated', { cap: ev.cap })}</Alert>
          ) : null}
          {ev.incomplete ? <Alert kind="warning">{t('deployments.eval.incomplete')}</Alert> : null}
          {ev.byPlatform || ev.byCompliance ? (
            <div className="deployments-breakdowns">
              <Bars
                title={t('deployments.eval.byPlatform')}
                entries={breakdown(ev.byPlatform)}
                prefix="deployments.platform"
              />
              <Bars
                title={t('deployments.eval.byCompliance')}
                entries={breakdown(ev.byCompliance)}
                prefix="deployments.compliance"
              />
            </div>
          ) : (
            <p className="deployments-muted">{t('deployments.eval.countsOnly')}</p>
          )}
          <h3>{t('deployments.eval.examples')}</h3>
          {(ev.examples ?? []).length === 0 ? (
            <p className="deployments-muted">{t('deployments.eval.noExamples')}</p>
          ) : (
            <ul className="deployments-examples">
              {(ev.examples ?? []).map((example) => (
                <li key={example.deviceId}>
                  {example.redacted || !example.name ? (
                    <span>{t('deployments.eval.redacted')}</span>
                  ) : linkDevices ? (
                    <Link to={`/devices/${enc(example.deviceId)}`}>{example.name}</Link>
                  ) : (
                    <span>{example.name}</span>
                  )}
                  <span className="deployments-muted">
                    {t(
                      codeKey(
                        'deployments.platform',
                        example.osPlatform,
                        'deployments.unknownValue',
                      ),
                    )}{' '}
                    ·{' '}
                    {t(
                      codeKey(
                        'deployments.compliance',
                        example.complianceState,
                        'deployments.unknownValue',
                      ),
                    )}
                  </span>
                  <Button
                    className="deployments-link-button"
                    onClick={() => onExplain(example.deviceId)}
                  >
                    {t('deployments.explain.why')}
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <p className="deployments-muted">
            {t('deployments.eval.at')}{' '}
            <time dateTime={ev.evaluatedAt}>{formatDateTime(locale, ev.evaluatedAt)}</time>
          </p>
        </div>
      ) : null}
      <Button onClick={() => onExplain()}>{t('deployments.explain.open')}</Button>
    </section>
  );
}

/** "Why is this Device (not) in the set?" per clause. */
export function ExplainDialog({
  targetSetId,
  deviceId: initialDevice,
  onClose,
}: {
  targetSetId: string;
  deviceId?: string | undefined;
  onClose: () => void;
}) {
  const { t, locale } = useI18n();
  const [device, setDevice] = useState<{ id: string; label: string } | undefined>(
    initialDevice ? { id: initialDevice, label: '' } : undefined,
  );
  const result = useAsync(
    (signal) =>
      device ? targetSetsApi.explain(targetSetId, device.id, signal) : Promise.resolve(undefined),
    [targetSetId, device?.id],
  );
  const ex = result.data;
  const name = ex
    ? ex.redacted || !ex.deviceName
      ? t('deployments.eval.redacted')
      : ex.deviceName
    : '';
  return (
    <Dialog title={t('deployments.explain.title')} onClose={onClose} wide>
      <div className="dialog-body">
        <DevicePicker
          label={t('deployments.explain.device')}
          onPick={(item) => setDevice({ id: item.id, label: item.label })}
        />
        {result.error ? <ApiErrorAlert error={result.error} onRetry={result.reload} /> : null}
        {device && result.loading ? <Skeleton lines={3} /> : null}
        {!device ? (
          <EmptyState
            title={t('deployments.explain.pick')}
            description={t('deployments.explain.pickHint')}
          />
        ) : null}
        {ex ? (
          <div aria-live="polite">
            <p className="deployments-explain-verdict">
              <StatusBadge tone={ex.matched ? 'success' : 'neutral'}>
                {ex.matched ? t('deployments.explain.in') : t('deployments.explain.out')}
              </StatusBadge>{' '}
              <strong>{name}</strong>
            </p>
            {ex.incomplete ? (
              <Alert kind="warning">{t('deployments.eval.incomplete')}</Alert>
            ) : null}
            <table className="deployments-clauses">
              <caption className="visually-hidden">{t('deployments.explain.clauses')}</caption>
              <thead>
                <tr>
                  <th scope="col">{t('deployments.explain.clause')}</th>
                  <th scope="col">{t('deployments.explain.result')}</th>
                  <th scope="col">{t('deployments.explain.reason')}</th>
                </tr>
              </thead>
              <tbody>
                {ex.clauses.map((clause, index) => (
                  <tr key={`${clause.clause}-${index}`}>
                    <td>
                      {t(codeKey('deployments.clause', clause.clause, 'deployments.unknownValue'))}
                    </td>
                    <td>
                      <StatusBadge tone={clause.result === 'matched' ? 'success' : 'neutral'}>
                        {t(`deployments.clauseResult.${clause.result}` as MessageKey)}
                      </StatusBadge>
                    </td>
                    <td>
                      {clause.reason
                        ? t(
                            codeKey(
                              'deployments.clauseReason',
                              clause.reason,
                              'deployments.unknownValue',
                            ),
                          )
                        : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            <p className="deployments-muted">
              {t('deployments.explain.rule')}{' '}
              <time dateTime={ex.evaluatedAt}>{formatDateTime(locale, ex.evaluatedAt)}</time>
            </p>
          </div>
        ) : null}
      </div>
      <div className="dialog-actions">
        <Button onClick={onClose}>{t('action.close')}</Button>
      </div>
    </Dialog>
  );
}

/** Section card with a heading. */
export function Section({
  title,
  children,
  actions,
  className = '',
}: {
  title: string;
  children: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  const headingId = useId();
  return (
    <section className={`deployments-card ${className}`} aria-labelledby={headingId}>
      <div className="deployments-card-heading">
        <h2 id={headingId}>{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  );
}
