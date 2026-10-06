import { TableDate } from '../../platform/ui/TableDate';
import type { MessageKey } from '../../platform/i18n/i18n';
import { DateFilter, FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { FormEvent } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { formatDateTime, formatJson } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { emptyForm, toAuditFilter, type FormState } from './auditFilter';
import { auditApi } from './api';
import type { AuditEvent, AuditFilter } from './types';

function actorOf(event: AuditEvent, system: string): string {
  if (event.actorId) return event.actorId;
  const actor = event.metadata.actor;
  return typeof actor === 'string' && actor ? actor : system;
}

function EventDetail({ event, onClose }: { event: AuditEvent; onClose: () => void }) {
  const { t, locale } = useI18n();
  const sections: Array<[string, unknown]> = [
    [t('audit.detail.before'), event.before],
    [t('audit.detail.after'), event.after],
    [t('audit.detail.metadata'), event.metadata],
  ];
  return (
    <Dialog title={t('audit.detail.title')} onClose={onClose} wide>
      <dl className="facts">
        <dt>{t('audit.col.time')}</dt>
        <dd>{formatDateTime(locale, event.occurredAt)}</dd>
        <dt>{t('audit.col.action')}</dt>
        <dd>
          <code>{event.action}</code>
        </dd>
        <dt>{t('audit.col.target')}</dt>
        <dd>
          <code>
            {event.targetType}:{event.targetId}
          </code>
        </dd>
        <dt>{t('audit.col.actor')}</dt>
        <dd>{actorOf(event, t('audit.actor.system'))}</dd>
        <dt>{t('audit.col.correlation')}</dt>
        <dd>
          <code>{event.correlationId}</code>
        </dd>
        <dt>{t('audit.detail.id')}</dt>
        <dd>
          <code>{event.id}</code>
        </dd>
      </dl>
      {sections.map(([label, value]) => (
        <section key={label}>
          <h3>{label}</h3>
          <pre className="json" tabIndex={0}>
            {formatJson(value)}
          </pre>
        </section>
      ))}
      <div className="dialog-actions">
        <Button variant="primary" onClick={onClose} autoFocus>
          {t('action.close')}
        </Button>
      </div>
    </Dialog>
  );
}

export function AuditScreen() {
  const { t } = useI18n();
  const [form, setForm] = useState<FormState>(emptyForm);
  const [applied, setApplied] = useState<AuditFilter>({});
  const [selected, setSelected] = useState<AuditEvent | null>(null);
  const list = usePagedList(
    (cursor, signal) => auditApi.events(applied, cursor, signal),
    [applied],
  );

  const set = (key: keyof FormState) => (event: { target: { value: string } }) =>
    setForm((previous) => ({ ...previous, [key]: event.target.value }));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    setApplied(toAuditFilter(form));
  };
  const reset = () => {
    setForm(emptyForm);
    setApplied({});
  };

  const columns: Column<AuditEvent>[] = [
    {
      key: 'time',
      header: t('audit.col.time'),
      render: (e) => <TableDate value={e.occurredAt} />,
    },
    { key: 'action', header: t('audit.col.action'), render: (e) => <code>{e.action}</code> },
    {
      key: 'target',
      header: t('audit.col.target'),
      render: (e) => (
        <code>
          {e.targetType}:{e.targetId}
        </code>
      ),
    },
    {
      key: 'actor',
      header: t('audit.col.actor'),
      render: (e) => actorOf(e, t('audit.actor.system')),
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

  return (
    <>
      <PageHeader title={t('nav.audit')} intro={t('audit.intro')} />
      <FilterBar
        activeFilters={Object.entries(applied).map(([key, value]) => ({
          key,
          label: `${t(`audit.filter.${key}` as MessageKey)}: ${value}`,
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

        <Button type="submit" variant="primary">
          {t('filters.apply')}
        </Button>
        <Button onClick={reset}>{t('filters.reset')}</Button>
      </FilterBar>
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
      {selected ? <EventDetail event={selected} onClose={() => setSelected(null)} /> : null}
    </>
  );
}
