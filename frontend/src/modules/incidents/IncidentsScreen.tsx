import { TableDate } from '../../platform/ui/TableDate';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, TextArea, TextField } from '../../platform/ui/Field';
import { Select } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TicketPicker } from '../tickets/TicketPicker';
import { organizationApi } from '../organization/api';
import type { TicketHit } from '../tickets/ticketLookup';
import { incidentsApi, type MajorIncident } from './api';
import { incidentLocationNames } from './incidentModel';

export function ExerciseBadge() {
  const { t } = useI18n();
  return <Badge tone="info">{t('incidents.exercise')}</Badge>;
}

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
  const [exercise, setExercise] = useState(false);
  const [tickets, setTickets] = useState<TicketHit[]>([]);
  const [locations, setLocations] = useState<Array<{ id: string; name: string }>>([]);
  const [locationQuery, setLocationQuery] = useState('');
  const debouncedLocationQuery = useDebouncedValue(locationQuery.trim(), 250);
  const locationResults = useAsync(
    async (signal) =>
      debouncedLocationQuery
        ? (await organizationApi.searchLocations(debouncedLocationQuery, signal)).items.filter(
            (location) =>
              location.active && !locations.some((selected) => selected.id === location.id),
          )
        : [],
    [debouncedLocationQuery, locations.map((location) => location.id).join(',')],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [unlinked, setUnlinked] = useState<TicketHit[]>([]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      const incident = await incidentsApi.declare(
        title.trim(),
        message.trim(),
        exercise,
        locations.map((location) => location.id),
      );
      // The declaration is committed; a ticket that cannot be linked is reported, not retried silently.
      const failed: TicketHit[] = [];
      for (const hit of tickets) {
        try {
          await incidentsApi.linkTicket(incident.id, hit.id);
        } catch {
          failed.push(hit);
        }
      }
      if (failed.length > 0) {
        setUnlinked(failed);
        return;
      }
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  if (unlinked.length > 0)
    return (
      <Dialog title={t('incidents.declare')} onClose={onDone}>
        <Alert kind="warning">
          {t('incidents.declare.unlinked', {
            references: unlinked.map((hit) => hit.reference).join(', '),
          })}
        </Alert>
        <div className="dialog-actions">
          <Button variant="primary" onClick={onDone}>
            {t('action.close')}
          </Button>
        </div>
      </Dialog>
    );
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
        <Checkbox
          label={t('incidents.declare.exercise')}
          description={t('incidents.declare.exercise.hint')}
          checked={exercise}
          onChange={(event) => setExercise(event.target.checked)}
        />
        <TicketPicker
          label={t('incidents.declare.tickets')}
          hint={t('incidents.declare.tickets.hint')}
          multiple
          selected={tickets}
          disabled={busy}
          onChange={setTickets}
        />
        <TextField
          label={t('incidents.locations.search')}
          type="search"
          value={locationQuery}
          maxLength={100}
          onChange={(event) => setLocationQuery(event.target.value)}
        />
        {locationResults.error ? (
          <ApiErrorAlert error={locationResults.error} onRetry={locationResults.reload} />
        ) : null}
        {(locationResults.data ?? []).length > 0 ? (
          <Select
            label={t('incidents.locations.pick')}
            value=""
            onChange={(event) => {
              const location = (locationResults.data ?? []).find(
                (item) => item.id === event.target.value,
              );
              if (location) {
                setLocations((current) => [...current, { id: location.id, name: location.name }]);
                setLocationQuery('');
              }
            }}
            options={[
              { value: '', label: '-' },
              ...(locationResults.data ?? []).map((location) => ({
                value: location.id,
                label: location.name,
              })),
            ]}
          />
        ) : null}
        {locations.length > 0 ? (
          <ul className="plain-list">
            {locations.map((location) => (
              <li key={location.id}>
                {location.name}{' '}
                <button
                  type="button"
                  className="btn-link"
                  onClick={() =>
                    setLocations((current) => current.filter((item) => item.id !== location.id))
                  }
                >
                  {t('incidents.locations.remove')}
                </button>
              </li>
            ))}
          </ul>
        ) : null}
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
      render: (m) => (
        <>
          <Link to={`/incidents/${encodeURIComponent(m.id)}`}>{m.title}</Link>
          {m.isExercise ? (
            <>
              {' '}
              <ExerciseBadge />
            </>
          ) : null}
        </>
      ),
    },
    {
      key: 'status',
      header: t('incidents.col.status'),
      render: (m) => <IncidentBadge status={m.status} />,
    },
    { key: 'tickets', header: t('incidents.col.tickets'), render: (m) => m.linkedTickets },
    {
      key: 'locations',
      header: t('incidents.locations'),
      render: (m) =>
        incidentLocationNames(
          m.locations,
          m.affectedLocationCount,
          t('incidents.locations.unavailable'),
        ) ?? t('incidents.locations.none'),
    },
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
