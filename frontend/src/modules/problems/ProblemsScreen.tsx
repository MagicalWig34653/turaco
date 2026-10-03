import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { problemsApi, problemStatuses, type Problem, type ProblemStatus } from './api';

export function ProblemBadge({ status }: { status: ProblemStatus }) {
  const { t } = useI18n();
  const tone =
    status === 'resolved' || status === 'closed'
      ? 'success'
      : status === 'known_error' || status === 'resolution_planned'
        ? 'info'
        : 'warning';
  return <Badge tone={tone}>{t(`problems.status.${status}`)}</Badge>;
}

function NewDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const created = await problemsApi.create(title.trim(), description.trim());
      navigate(`/problems/${encodeURIComponent(created.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('problems.new')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('problems.field.title')}
          value={title}
          maxLength={200}
          required
          autoFocus
          onChange={(event) => setTitle(event.target.value)}
        />
        <TextArea
          label={t('problems.field.description')}
          value={description}
          rows={4}
          maxLength={5000}
          onChange={(event) => setDescription(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={title.trim() === ''}>
            {t('problems.new')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function ProblemsScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState<ProblemStatus | ''>('');
  const [creating, setCreating] = useState(false);
  const list = usePagedList((cursor, signal) => problemsApi.list(status, cursor, signal), [status]);
  const columns: Column<Problem>[] = [
    {
      key: 'ref',
      header: t('problems.col.reference'),
      render: (p) => <Link to={`/problems/${encodeURIComponent(p.id)}`}>{p.reference}</Link>,
    },
    { key: 'title', header: t('problems.col.title'), render: (p) => p.title },
    {
      key: 'status',
      header: t('problems.col.status'),
      render: (p) => <ProblemBadge status={p.status} />,
    },
    { key: 'tickets', header: t('problems.col.tickets'), render: (p) => p.linkedTickets },
    {
      key: 'updated',
      header: t('problems.col.updated'),
      render: (p) => formatDateTime(locale, p.updatedAt),
    },
  ];
  return (
    <>
      <PageHeader
        title={t('nav.problems')}
        intro={t('problems.intro')}
        actions={
          can('problems.manage') ? (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('problems.new')}
            </Button>
          ) : null
        }
      />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <Select
          label={t('problems.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as ProblemStatus | '')}
          options={[
            { value: '', label: t('problems.filter.any') },
            ...problemStatuses.map((value) => ({ value, label: t(`problems.status.${value}`) })),
          ]}
        />
      </form>
      <DataTable
        caption={t('nav.problems')}
        columns={columns}
        rows={list.items}
        rowKey={(p) => p.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('problems.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {creating ? <NewDialog onClose={() => setCreating(false)} /> : null}
    </>
  );
}
