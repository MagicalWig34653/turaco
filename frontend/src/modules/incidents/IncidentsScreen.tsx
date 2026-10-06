import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { incidentsApi, type MajorIncident } from './api';

export function IncidentBadge({ status }: { status: MajorIncident['status'] }) {
  const { t } = useI18n();
  const tone =
    status === 'resolved' || status === 'closed'
      ? 'success'
      : status === 'monitoring'
        ? 'info'
        : 'warning';
  return <Badge tone={tone}>{t(`incidents.status.${status}`)}</Badge>;
}

function DeclareDialog({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const { t } = useI18n();
  const [title, setTitle] = useState('');
  const [message, setMessage] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await incidentsApi.declare(title.trim(), message.trim());
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={t('incidents.declare')} onClose={onClose}>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('incidents.field.title')}
          value={title}
          maxLength={200}
          required
          autoFocus
          onChange={(event) => setTitle(event.target.value)}
        />
        <TextArea
          label={t('incidents.field.message')}
          hint={t('incidents.field.message.hint')}
          value={message}
          maxLength={2000}
          rows={4}
          required
          onChange={(event) => setMessage(event.target.value)}
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant="danger"
            busy={busy}
            disabled={title.trim() === '' || message.trim() === ''}
          >
            {t('incidents.declare')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function IncidentsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [activeOnly, setActiveOnly] = useState(true);
  const [declaring, setDeclaring] = useState(false);
  const list = usePagedList(
    (cursor, signal) => incidentsApi.list(activeOnly, cursor, signal),
    [activeOnly],
  );
  const columns: Column<MajorIncident>[] = [
    {
      key: 'title',
      header: t('incidents.col.title'),
      render: (m) => <Link to={`/incidents/${encodeURIComponent(m.id)}`}>{m.title}</Link>,
    },
    {
      key: 'status',
      header: t('incidents.col.status'),
      render: (m) => <IncidentBadge status={m.status} />,
    },
    { key: 'tickets', header: t('incidents.col.tickets'), render: (m) => m.linkedTickets },
    {
      key: 'updated',
      header: t('incidents.col.updated'),
      render: (m) => <TableDate value={m.updatedAt} />,
    },
  ];
  const activeFilters = [
    ...(activeOnly
      ? [
          {
            key: 'activeOnly',
            label: t('incidents.filter.activeOnly'),
            onRemove: () => {
              setActiveOnly(false);
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('nav.incidents')}
        intro={t('incidents.intro')}
        actions={
          can('majorincidents.manage') ? (
            <Button variant="danger" onClick={() => setDeclaring(true)}>
              {t('incidents.declare')}
            </Button>
          ) : null
        }
      />
      <FilterBar activeFilters={activeFilters} onSubmit={(event) => event.preventDefault()}>
        <Checkbox
          label={t('incidents.filter.activeOnly')}
          checked={activeOnly}
          onChange={(event) => setActiveOnly(event.target.checked)}
        />
      </FilterBar>
      <DataTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.incidents')}
        columns={columns}
        rows={list.items}
        rowKey={(m) => m.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('incidents.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {declaring ? (
        <DeclareDialog
          onClose={() => setDeclaring(false)}
          onDone={() => {
            setDeclaring(false);
            list.reload();
          }}
        />
      ) : null}
    </>
  );
}
