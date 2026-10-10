import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime, formatJson, retryAfterMinutes } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { en } from '../../platform/i18n/messages.en';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { DateFilter, FilterBar } from '../../platform/ui/FilterBar';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import '../health/health.css';
import { auditApi } from './api';
import {
  actorKinds,
  auditModules,
  systemActors,
  toAuditFilter,
  viaValues,
  type FormState,
} from './auditFilter';
import {
  actorView,
  defaultForm,
  diffRows,
  exportErrorKey,
  nameOf,
  osUserOf,
  rangeFormHours,
  rangeProblem,
  tooLargeCount,
  visibleMetadata,
  type Named,
} from './auditModel';
import type { AuditEvent, AuditFilter } from './types';

const keyOr = (key: string, fallback: MessageKey): MessageKey =>
  Object.hasOwn(en, key) ? (key as MessageKey) : fallback;

/** A resolved name with the raw id on hover; a removed entity reads "Removed (suffix)". */
function NamedText({ named }: { named: Named }) {
  const { t } = useI18n();
  if (!named.id) return <span>–</span>;
  return (
    <span title={named.id}>{named.gone ? t('audit.removed', { id: named.name }) : named.name}</span>
  );
}

function Actor({ event }: { event: AuditEvent }) {
  const { t } = useI18n();
  const view = actorView(event);
  switch (view.kind) {
    case 'user':
      return <NamedText named={view.named} />;
    case 'system':
      return (
        <>
          {t(keyOr(`audit.systemActor.${view.name}`, 'audit.systemActor.unknown'), {
            name: view.name,
          })}
        </>
      );
    case 'metadata':
      return <>{view.name}</>;
    default:
      return <>{t('audit.actor.system')}</>;
  }
}

function Target({ event }: { event: AuditEvent }) {
  return (
    <span>
      <code>{event.targetType}</code> <NamedText named={nameOf(event.target, event.targetId)} />
    </span>
  );
}

function ViaBadge({ via }: { via: AuditEvent['via'] }) {
  const { t } = useI18n();
  return via ? <Badge tone="info">{t(`audit.via.${via}`)}</Badge> : null;
}

function Diff({ event }: { event: AuditEvent }) {
  const { t } = useI18n();
  const rows = diffRows(event.before, event.after);
  if (event.before === undefined && event.after === undefined) {
    return <p className="health-meta">{t('audit.detail.noChange')}</p>;
  }
  return (
    <>
      {event.before === undefined ? (
        <p className="health-meta">{t('audit.detail.noBefore')}</p>
      ) : null}
      {event.after === undefined ? (
        <p className="health-meta">{t('audit.detail.noAfter')}</p>
      ) : null}
      <table className="audit-diff">
        <caption className="visually-hidden">{t('audit.detail.diff')}</caption>
        <thead>
          <tr>
            <th scope="col">{t('audit.detail.field')}</th>
            <th scope="col">{t('audit.detail.before')}</th>
            <th scope="col">{t('audit.detail.after')}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.path || '(value)'} className={`audit-diff-${row.kind}`}>
              <th scope="row">
                <code>{row.path || t('audit.detail.value')}</code>{' '}
                <span className="health-meta">{t(`audit.diff.${row.kind}`)}</span>
              </th>
              <td>{row.before ?? '–'}</td>
              <td>{row.after ?? '–'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

function EventDetail({
  event,
  onClose,
  onCorrelation,
}: {
  event: AuditEvent;
  onClose: () => void;
  onCorrelation: (id: string) => void;
}) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const canSeeOsUser = can('platform.audit.export');
  const osUser = canSeeOsUser ? osUserOf(event.metadata) : undefined;
  const actor = actorView(event);
  const actorId = actor.kind === 'user' ? actor.named.id : '';
  return (
    <Dialog title={t('audit.detail.title')} onClose={onClose} drawer>
      <dl className="facts">
        <dt>{t('audit.col.time')}</dt>
        <dd>{formatDateTime(locale, event.occurredAt)}</dd>
        <dt>{t('audit.col.action')}</dt>
        <dd>
          <code>{event.action}</code>
        </dd>
        <dt>{t('audit.col.target')}</dt>
        <dd>
          <Target event={event} />
          <br />
          <code>
            {event.targetType}:{event.targetId}
          </code>
        </dd>
        <dt>{t('audit.col.actor')}</dt>
        <dd>
          <Actor event={event} /> <ViaBadge via={event.via} />
          {actorId ? (
            <>
              <br />
              <code>{actorId}</code>
            </>
          ) : null}
        </dd>
        {osUser ? (
          <>
            <dt>{t('audit.detail.osUser')}</dt>
            <dd>
              <code>{osUser}</code>
            </dd>
          </>
        ) : null}
        <dt>{t('audit.col.correlation')}</dt>
        <dd>
          <code>{event.correlationId}</code>{' '}
          <Button onClick={() => onCorrelation(event.correlationId)}>
            {t('audit.detail.sameRequest')}
          </Button>
        </dd>
        <dt>{t('audit.detail.id')}</dt>
        <dd>
          <code>{event.id}</code>
        </dd>
      </dl>
      <section>
        <h3>{t('audit.detail.changes')}</h3>
        <Diff event={event} />
      </section>
      <section>
        <h3>{t('audit.detail.metadata')}</h3>
        <pre className="json" tabIndex={0}>
          {formatJson(visibleMetadata(event.metadata, canSeeOsUser))}
        </pre>
      </section>
      <div className="dialog-actions">
        <Button variant="primary" onClick={onClose} autoFocus>
          {t('action.close')}
        </Button>
      </div>
    </Dialog>
  );
}

function download(blob: Blob, name: string) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = name;
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}

function ExportControls({ filter }: { filter: AuditFilter }) {
  const { t } = useI18n();
  const [details, setDetails] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [problem, setProblem] = useState<ReturnType<typeof rangeProblem>>(undefined);
  const [done, setDone] = useState(false);
  const run = async () => {
    setError(undefined);
    setDone(false);
    const issue = rangeProblem(filter);
    setProblem(issue);
    if (issue) return;
    setBusy(true);
    try {
      const blob = await auditApi.exportCsv(filter, details);
      download(blob, 'audit-events.csv');
      setDone(true);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const key = error ? exportErrorKey(error) : undefined;
  const count = error ? tooLargeCount(error) : undefined;
  return (
    <section className="audit-export" aria-label={t('audit.export.title')}>
      <Checkbox
        label={t('audit.export.details')}
        description={t('audit.export.detailsHint')}
        checked={details}
        onChange={(event) => setDetails(event.target.checked)}
      />
      <Button variant="primary" busy={busy} onClick={() => void run()}>
        {t('audit.export.button')}
      </Button>
      {problem ? (
        <Alert kind="error">
          {t(
            problem === 'required'
              ? 'audit.export.error.rangeRequired'
              : problem === 'tooLong'
                ? 'audit.export.error.rangeTooLong'
                : 'audit.export.error.rangeInverted',
          )}
        </Alert>
      ) : null}
      {error && key ? (
        <Alert kind="error">
          <p>{t(key)}</p>
          {count !== undefined ? <p>{t('audit.export.error.count', { count })}</p> : null}
          {error.status === 429 ? (
            <p>
              {t('audit.export.error.retryAfter', {
                minutes: retryAfterMinutes(error.retryAfterSeconds),
              })}
            </p>
          ) : null}
        </Alert>
      ) : error ? (
        <ApiErrorAlert error={error} />
      ) : null}
      {done ? <Alert kind="success">{t('audit.export.done')}</Alert> : null}
    </section>
  );
}

function RetentionLine() {
  const { t, locale } = useI18n();
  const retention = useAsync((signal) => auditApi.retention(signal), []);
  const info = retention.data;
  if (retention.error || !info) return null;
  return (
    <p className="audit-retention">
      {info.retentionDays > 0
        ? t('audit.retention.days', { days: info.retentionDays })
        : t('audit.retention.forever')}
      {' · '}
      {info.oldestEventAt
        ? t('audit.retention.oldest', { date: formatDateTime(locale, info.oldestEventAt) })
        : t('audit.retention.noEvents')}
    </p>
  );
}

export function AuditScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [form, setForm] = useState<FormState>(() => defaultForm());
  const [applied, setApplied] = useState<AuditFilter>(() => toAuditFilter(defaultForm()));
  const [selected, setSelected] = useState<AuditEvent | null>(null);
  const [unresolved, setUnresolved] = useState<string[]>([]);
  const list = usePagedList(
    (cursor, signal) =>
      auditApi.events(applied, cursor, signal).then((page) => {
        const types = page.unresolvedTypes ?? [];
        setUnresolved((previous) =>
          cursor ? [...new Set([...previous, ...types])] : [...new Set(types)],
        );
        return page;
      }),
    [applied],
  );

  const set = (key: keyof FormState) => (event: { target: { value: string } }) =>
    setForm((previous) => ({ ...previous, [key]: event.target.value }));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setApplied(toAuditFilter(form));
  };
  const reset = () => {
    const next = defaultForm();
    setForm(next);
    setApplied(toAuditFilter(next));
  };
  const quickRange = (hours: number) => {
    const next = { ...form, ...rangeFormHours(hours) };
    setForm(next);
    setApplied(toAuditFilter(next));
  };
  const showRequest = (correlationId: string) => {
    const next = { ...form, correlationId };
    setForm(next);
    setApplied(toAuditFilter(next));
    setSelected(null);
  };

  const filterLabel = (key: string, value: string) => {
    const base = t(keyOr(`audit.filter.${key}`, 'audit.filter.actionPrefix'));
    if (key === 'from' || key === 'to') return `${base}: ${value}`;
    return `${base}: ${value}`;
  };

  const columns: Column<AuditEvent>[] = [
    {
      key: 'time',
      header: t('audit.col.time'),
      render: (e) => <TableDate value={e.occurredAt} />,
    },
    { key: 'action', header: t('audit.col.action'), render: (e) => <code>{e.action}</code> },
    { key: 'target', header: t('audit.col.target'), render: (e) => <Target event={e} /> },
    {
      key: 'actor',
      header: t('audit.col.actor'),
      render: (e) => (
        <>
          <Actor event={e} /> <ViaBadge via={e.via} />
        </>
      ),
    },
    {
      key: 'details',
      header: t('audit.col.details'),
      render: (e) => (
        <Button
          onClick={() => setSelected(e)}
          aria-label={t('audit.details.for', { action: e.action })}
        >
          {t('audit.details')}
        </Button>
      ),
    },
  ];

  const options = (values: readonly string[], label: (value: string) => string) => [
    { value: '', label: t('filters.all') },
    ...values.map((value) => ({ value, label: label(value) })),
  ];

  return (
    <>
      <PageHeader title={t('nav.audit')} intro={t('audit.intro')} />
      <RetentionLine />
      <FilterBar
        activeFilters={Object.entries(applied).map(([key, value]) => ({
          key,
          label: filterLabel(key, String(value)),
          onRemove: () => {
            setApplied((current) => {
              const next = { ...current };
              delete next[key as keyof AuditFilter];
              return next;
            });
            setForm((current) => ({ ...current, [key]: '' }));
          },
        }))}
        onClear={reset}
        onSubmit={submit}
        aria-label={t('filters.title')}
      >
        <div className="audit-range" role="group" aria-label={t('audit.range')}>
          <DateFilter
            label={t('audit.filter.from')}
            type="datetime-local"
            value={form.from}
            onChange={(value) => setForm((previous) => ({ ...previous, from: value }))}
          />
          <DateFilter
            label={t('audit.filter.to')}
            type="datetime-local"
            value={form.to}
            onChange={(value) => setForm((previous) => ({ ...previous, to: value }))}
          />
          <Button onClick={() => quickRange(1)}>{t('audit.range.hour')}</Button>
          <Button onClick={() => quickRange(24)}>{t('audit.range.day')}</Button>
          <Button onClick={() => quickRange(7 * 24)}>{t('audit.range.week')}</Button>
          <Button onClick={() => quickRange(30 * 24)}>{t('audit.range.month')}</Button>
        </div>
        <div className="audit-chips">
          <Select
            label={t('audit.filter.module')}
            value={form.module}
            onChange={set('module')}
            options={options(auditModules, (v) => v)}
          />
          <Select
            label={t('audit.filter.actorKind')}
            value={form.actorKind}
            onChange={set('actorKind')}
            options={options(actorKinds, (v) =>
              t(v === 'user' ? 'audit.actorKind.user' : 'audit.actorKind.system'),
            )}
          />
          <Select
            label={t('audit.filter.systemActor')}
            value={form.systemActor}
            onChange={set('systemActor')}
            options={options(systemActors, (v) =>
              t(keyOr(`audit.systemActor.${v}`, 'audit.systemActor.unknown'), { name: v }),
            )}
          />
          <Select
            label={t('audit.filter.via')}
            value={form.via}
            onChange={set('via')}
            options={options(viaValues, (v) => t(v === 'ai' ? 'audit.via.ai' : 'audit.via.mcp'))}
          />
        </div>
        <TextField
          label={t('audit.filter.actionPrefix')}
          hint={t('audit.filter.actionPrefix.hint')}
          value={form.actionPrefix}
          onChange={set('actionPrefix')}
          maxLength={200}
        />
        <TextField
          label={t('audit.filter.targetType')}
          value={form.targetType}
          onChange={set('targetType')}
          maxLength={100}
        />
        <TextField
          label={t('audit.filter.targetId')}
          value={form.targetId}
          onChange={set('targetId')}
          maxLength={200}
        />
        <TextField
          label={t('audit.filter.actorId')}
          value={form.actorId}
          onChange={set('actorId')}
        />
        <TextField
          label={t('audit.filter.correlationId')}
          value={form.correlationId}
          onChange={set('correlationId')}
          maxLength={200}
        />

        <Button type="submit" variant="primary">
          {t('filters.apply')}
        </Button>
        <Button onClick={reset}>{t('filters.reset')}</Button>
      </FilterBar>
      {can('platform.audit.export') ? <ExportControls filter={applied} /> : null}
      {unresolved.length > 0 ? (
        <Alert kind="info">{t('audit.unresolved', { types: unresolved.join(', ') })}</Alert>
      ) : null}
      <DataTable
        caption={t('nav.audit')}
        columns={columns}
        rows={list.items}
        rowKey={(e) => e.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('audit.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {selected ? (
        <EventDetail
          event={selected}
          onClose={() => setSelected(null)}
          onCorrelation={showRequest}
        />
      ) : null}
    </>
  );
}
