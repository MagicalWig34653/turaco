import { organizationApi } from '../organization/api';
import { useRef, useState, type ReactNode } from 'react';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { formatDateTime } from '../../platform/format/format';
import { PageHeader } from '../../platform/ui/PageHeader';
import { DataTable } from '../../platform/ui/DataTable';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Card, Skeleton, StatusBadge } from '../../platform/ui/Workspace';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { presenceApi } from './api';
import { usePresence } from './PresenceProvider';
import { calendarWindow, entryActions, entriesOnDay } from './model';
import { EntryDialog, LocationPicker } from './EntryDialog';
import type { Entry, Minimum, Operation, Settings } from './types';
import './presence.css';
function PrivacyNotice() {
  const { t } = useI18n();
  return <p className="presence-notice">{t('presence.privacy')}</p>;
}
function WindowPicker({
  anchor,
  setAnchor,
  view,
  setView,
}: {
  anchor: string;
  setAnchor: (date: string) => void;
  view: 'week' | 'month';
  setView?: (view: 'week' | 'month') => void;
}) {
  const { t } = useI18n();
  return (
    <div className="presence-toolbar">
      <TextField
        label={t('presence.date')}
        type="date"
        min={new Date().toISOString().slice(0, 10)}
        value={anchor}
        required
        onChange={(e) => {
          if (e.target.value && e.target.validity.valid) setAnchor(e.target.value);
        }}
      />
      {setView ? (
        <Select
          label={t('presence.view')}
          value={view}
          onChange={(e) => setView(e.target.value as 'week' | 'month')}
          options={(['week', 'month'] as const).map((value) => ({
            value,
            label: t(`presence.${value}`),
          }))}
        />
      ) : null}
      <p className="field-hint">{t('presence.windowHint')}</p>
    </div>
  );
}
export function MyPresenceScreen() {
  const { can } = usePresence();
  return can('presence.manage_own') ? <MyPresence /> : null;
}
function MyPresence() {
  const { t, locale } = useI18n();
  const { can } = usePresence();
  const [anchor, setAnchor] = useState(new Date().toISOString().slice(0, 10));
  const [view, setView] = useState<'week' | 'month'>('week');
  const window = calendarWindow(anchor, view);
  const loaded = useAsync(
    (signal) => presenceApi.entries(window.from, window.to, signal),
    [window.from, window.to],
  );
  const locationIds = [
    ...new Set(
      (loaded.data?.items ?? []).flatMap((entry) => (entry.locationId ? [entry.locationId] : [])),
    ),
  ]
    .sort()
    .join(',');
  const locations = useAsync(
    async (signal) =>
      can('organization.view') && locationIds
        ? Promise.all(
            locationIds
              .split(',')
              .map((id) => organizationApi.location(id, signal).catch(() => null)),
          )
        : [],
    [locationIds, can('organization.view')],
  );
  const [dialog, setDialog] = useState<{ operation: Operation; entry?: Entry }>();
  const rows = window.days.flatMap<{
    day: string;
    entry: Entry | undefined;
    occurrence: { from: string; to: string } | undefined;
  }>((day) => {
    const occurrences = entriesOnDay(loaded.data?.items ?? [], day);
    return occurrences.length
      ? occurrences.map(({ entry, occurrence }) => ({
          day,
          entry: entry as Entry | undefined,
          occurrence,
        }))
      : [{ day, entry: undefined, occurrence: undefined }];
  });
  return (
    <div className="presence-screen">
      <PageHeader
        title={t('presence.mine')}
        actions={
          <Button variant="primary" onClick={() => setDialog({ operation: 'create' })}>
            {t('presence.action.create')}
          </Button>
        }
      />
      <PrivacyNotice />
      <WindowPicker anchor={anchor} setAnchor={setAnchor} view={view} setView={setView} />
      <Button onClick={loaded.reload} disabled={loaded.loading}>
        {t('presence.refresh')}
      </Button>
      <DataTable
        caption={t('presence.mine')}
        rows={loaded.loading ? [] : rows}
        rowKey={(row) => `${row.day}-${row.entry?.id ?? 'empty'}-${row.occurrence?.from ?? ''}`}
        loading={loaded.loading}
        error={loaded.error}
        onRetry={loaded.reload}
        emptyText={t('presence.empty')}
        columns={[
          {
            key: 'day',
            header: t('presence.date'),
            render: ({ day }) => (
              <time dateTime={day}>
                {new Intl.DateTimeFormat(locale, {
                  timeZone: 'UTC',
                  weekday: 'short',
                  month: 'short',
                  day: 'numeric',
                }).format(new Date(`${day}T12:00:00Z`))}
              </time>
            ),
          },
          {
            key: 'kind',
            header: t('presence.kind'),
            render: ({ entry }) =>
              entry ? (
                <>
                  <StatusBadge tone={entry.kind === 'unavailable' ? 'warning' : 'info'}>
                    {t(`presence.kind.${entry.kind}`)}
                  </StatusBadge>{' '}
                  {entry.locationType ? t(`presence.location.${entry.locationType}`) : null}
                  {entry.locationId ? (
                    <>
                      {' '}
                      ·{' '}
                      {locations.data?.find((location) => location?.id === entry.locationId)
                        ?.name ?? t('presence.currentLocation')}
                    </>
                  ) : null}
                  <br />
                  {t(`presence.visibility.${entry.visibility}`)}
                </>
              ) : (
                t('presence.empty')
              ),
          },
          {
            key: 'time',
            header: t('presence.time'),
            render: ({ entry, occurrence }) =>
              occurrence ? (
                <>
                  <time dateTime={occurrence.from}>{formatDateTime(locale, occurrence.from)}</time>{' '}
                  – <time dateTime={occurrence.to}>{formatDateTime(locale, occurrence.to)}</time>
                  {entry?.allDay ? (
                    <small>
                      {' '}
                      · {t('presence.allDay')} ({entry.timezone})
                    </small>
                  ) : null}
                  {entry?.recurrence ? (
                    <small> · {t(`presence.repeat.${entry.recurrence.frequency}`)}</small>
                  ) : null}
                </>
              ) : (
                '—'
              ),
          },
          {
            key: 'source',
            header: t('presence.source'),
            render: ({ entry }) =>
              entry ? (
                <>
                  {entry.source === 'manual' ? (
                    t('presence.manual')
                  ) : (
                    <>
                      {entry.source} · {t('presence.readOnly')}
                    </>
                  )}{' '}
                  · {t(`presence.status.${entry.status}`)}
                </>
              ) : (
                '—'
              ),
          },
        ]}
        rowActions={({ entry }) =>
          entry
            ? entryActions(entry).map((operation) => ({
                id: operation,
                label: t(`presence.action.${operation}`),
                danger: operation === 'cancel',
                onSelect: () => setDialog({ operation, entry }),
              }))
            : []
        }
      />
      {dialog ? (
        <EntryDialog
          {...dialog}
          onClose={() => setDialog(undefined)}
          onDone={() => {
            setDialog(undefined);
            loaded.reload();
          }}
        />
      ) : null}
    </div>
  );
}
/** Shared mutation frame guards double submits and retains the exact version after conflicts. */
function MutationDialog({
  title,
  children,
  onClose,
  submit,
  danger = false,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  submit: () => Promise<void>;
  danger?: boolean;
}) {
  const { t } = useI18n();
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  return (
    <Dialog
      title={title}
      onClose={() => {
        if (!pending.current) onClose();
      }}
    >
      <form
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          if (pending.current) return;
          pending.current = true;
          setBusy(true);
          setError(undefined);
          void submit()
            .catch((cause: unknown) => setError(asApiError(cause)))
            .finally(() => {
              pending.current = false;
              setBusy(false);
            });
        }}
      >
        {error ? <ApiErrorAlert error={error} /> : null}
        <fieldset disabled={busy} className="presence-fields">
          {children}
        </fieldset>
        <div className="dialog-actions">
          <Button disabled={busy} onClick={onClose}>
            {t('action.cancel')}
          </Button>
          <Button type="submit" busy={busy} variant={danger ? 'danger' : 'primary'}>
            {title}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
function MinimumDialog({
  teamId,
  minimum,
  onClose,
  onDone,
}: {
  teamId: string;
  minimum: Minimum | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [count, setCount] = useState(minimum?.minimum ?? 0);
  const [onsite, setOnsite] = useState(minimum?.onsiteMinimum?.toString() ?? '');
  const [locationId, setLocationId] = useState(minimum?.locationId ?? '');
  return (
    <MutationDialog
      title={t('presence.minimumEdit')}
      onClose={onClose}
      submit={async () => {
        await presenceApi.setMinimum(teamId, {
          minimum: count,
          ...(minimum ? { expectedVersion: minimum.version } : {}),
          ...(onsite !== '' ? { onsiteMinimum: Number(onsite) } : {}),
          ...(locationId ? { locationId } : {}),
        });
        onDone();
      }}
    >
      <TextField
        label={t('presence.minimum')}
        required
        type="number"
        min={0}
        max={1000}
        value={count}
        onChange={(e) => setCount(Number(e.target.value))}
      />
      <TextField
        label={t('presence.onsiteMinimum')}
        type="number"
        min={0}
        max={1000}
        value={onsite}
        onChange={(e) => setOnsite(e.target.value)}
      />
      <label>
        <input
          type="checkbox"
          checked={!!locationId || onsite !== ''}
          onChange={(e) => {
            if (!e.target.checked) {
              setLocationId('');
              setOnsite('');
            } else setOnsite('0');
          }}
        />{' '}
        {t('presence.configureOnsite')}
      </label>
      {onsite !== '' ? <LocationPicker value={locationId} onChange={setLocationId} /> : null}
    </MutationDialog>
  );
}
export function TeamCoverageScreen() {
  const { can } = usePresence();
  return can('presence.view_availability') || can('presence.manage_teams') ? (
    <TeamCoverage />
  ) : null;
}
function TeamCoverage() {
  const { t } = useI18n();
  const [team, setTeam] = useState<Assignee | null>(null);
  return (
    <div className="presence-screen">
      <PageHeader title={t('presence.team')} />
      <PrivacyNotice />
      <AssigneePicker type="team" value={team} onChange={setTeam} />
      {team ? <CoveragePanel key={team.id} teamId={team.id} /> : null}
    </div>
  );
}
function CoveragePanel({ teamId }: { teamId: string }) {
  const { t } = useI18n();
  const { can } = usePresence();
  const [anchor, setAnchor] = useState(new Date().toISOString().slice(0, 10));
  const [editing, setEditing] = useState(false);
  const window = calendarWindow(anchor, 'week');
  const coverage = useAsync(
    async (signal) =>
      can('presence.view_availability')
        ? presenceApi.coverage(teamId, window.from, window.to, signal)
        : null,
    [teamId, window.from, window.to, can('presence.view_availability')],
  );
  const minimum = useAsync((signal) => presenceApi.minimum(teamId, signal), [teamId]);
  return (
    <>
      <WindowPicker anchor={anchor} setAnchor={setAnchor} view="week" />
      <Button
        onClick={() => {
          minimum.reload();
          coverage.reload();
        }}
        disabled={minimum.loading || coverage.loading}
      >
        {t('presence.refresh')}
      </Button>
      {minimum.error ? (
        <ApiErrorAlert error={minimum.error} onRetry={minimum.reload} />
      ) : minimum.loading ? (
        <Skeleton lines={1} />
      ) : (
        <p>
          {t('presence.minimum')}: {minimum.data?.minimum?.minimum ?? t('presence.notSet')} ·{' '}
          {t('presence.onsiteMinimum')}:{' '}
          {minimum.data?.minimum?.onsiteMinimum ?? t('presence.notSet')}
        </p>
      )}
      {can('presence.manage_teams') && minimum.data && !minimum.loading ? (
        <Button onClick={() => setEditing(true)}>{t('presence.minimumEdit')}</Button>
      ) : null}
      {can('presence.view_availability') ? (
        <DataTable
          caption={t('presence.team')}
          rows={coverage.loading ? [] : (coverage.data?.days ?? [])}
          rowKey={(day) => day.date}
          loading={coverage.loading}
          error={coverage.error}
          onRetry={coverage.reload}
          emptyText={t('presence.coverage.unknown')}
          columns={[
            {
              key: 'date',
              header: t('presence.date'),
              render: (day) => <time dateTime={day.date}>{day.date}</time>,
            },
            ...(['available', 'limited', 'unavailable', 'unknown'] as const).map((key) => ({
              key,
              header: t(`presence.value.${key}`),
              render: (day: NonNullable<typeof coverage.data>['days'][number]) =>
                day[key] ?? t('presence.value.unknown'),
            })),
            {
              key: 'onsite',
              header: t('presence.onsite'),
              render: (day) => day.onsiteAvailable ?? t('presence.value.unknown'),
            },
            {
              key: 'state',
              header: t('presence.coverage'),
              render: (day) => (
                <StatusBadge
                  tone={
                    day.state === 'ok' ? 'success' : day.state === 'below' ? 'warning' : 'neutral'
                  }
                >
                  {t(`presence.coverage.${day.state}`)}
                </StatusBadge>
              ),
            },
          ]}
        />
      ) : null}
      {editing && minimum.data ? (
        <MinimumDialog
          teamId={teamId}
          minimum={minimum.data.minimum}
          onClose={() => setEditing(false)}
          onDone={() => {
            setEditing(false);
            minimum.reload();
            coverage.reload();
          }}
        />
      ) : null}
    </>
  );
}
export function PresenceAdminScreen() {
  const { can } = usePresence();
  return can('presence.admin') ? <PresenceAdmin /> : null;
}
function SettingsDialog({
  settings,
  onClose,
  onDone,
}: {
  settings: Settings;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(settings);
  return (
    <MutationDialog
      title={t('presence.settingsEdit')}
      onClose={onClose}
      submit={async () => {
        await presenceApi.saveSettings({
          enabled: draft.enabled,
          retentionDays: draft.retentionDays,
          externalSourcesEnabled: settings.externalSourcesEnabled,
          ...(draft.dpiaRecordedOn ? { dpiaRecordedOn: draft.dpiaRecordedOn } : {}),
          ...(draft.councilConfirmedOn ? { councilConfirmedOn: draft.councilConfirmedOn } : {}),
          expectedVersion: settings.version,
        });
        onDone();
      }}
    >
      <label>
        <input
          type="checkbox"
          checked={draft.enabled}
          onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })}
        />{' '}
        {t('presence.enabled')}
      </label>
      <p>{t('presence.disableHint')}</p>
      <TextField
        label={t('presence.retention')}
        type="number"
        min={1}
        max={Math.min(30, settings.maxRetentionDays)}
        required
        value={draft.retentionDays}
        onChange={(e) => setDraft({ ...draft, retentionDays: Number(e.target.value) })}
      />
      <TextField
        label={t('presence.dpia')}
        type="date"
        required={draft.enabled}
        value={draft.dpiaRecordedOn ?? ''}
        onChange={(e) => setDraft({ ...draft, dpiaRecordedOn: e.target.value })}
      />
      <TextField
        label={t('presence.council')}
        type="date"
        value={draft.councilConfirmedOn ?? ''}
        onChange={(e) => setDraft({ ...draft, councilConfirmedOn: e.target.value })}
      />
    </MutationDialog>
  );
}
function PresenceAdmin() {
  const { t } = useI18n();
  const { refresh } = usePresence();
  const loaded = useAsync((signal) => presenceApi.settings(signal), []);
  const [dialog, setDialog] = useState<'settings' | 'purge'>();
  const [purged, setPurged] = useState(false);
  const settings = loaded.data;
  return (
    <div className="presence-screen">
      <PageHeader title={t('presence.admin')} />
      <PrivacyNotice />
      <Button onClick={loaded.reload} disabled={loaded.loading}>
        {t('presence.refresh')}
      </Button>
      {loaded.error ? (
        <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />
      ) : !settings || loaded.loading ? (
        <Skeleton lines={3} />
      ) : (
        <Card>
          <dl>
            <dt>{t('presence.enabled')}</dt>
            <dd>{t(settings.enabled ? 'presence.on' : 'presence.off')}</dd>
            <dt>{t('presence.retention')}</dt>
            <dd>{settings.retentionDays}</dd>
            <dt>{t('presence.dpia')}</dt>
            <dd>{settings.dpiaRecordedOn ?? t('presence.notSet')}</dd>
            <dt>{t('presence.council')}</dt>
            <dd>{settings.councilConfirmedOn ?? t('presence.notSet')}</dd>
          </dl>
          <div className="presence-toolbar">
            <Button onClick={() => setDialog('settings')}>{t('presence.settingsEdit')}</Button>
            <Button variant="danger" onClick={() => setDialog('purge')}>
              {t('presence.purge')}
            </Button>
          </div>
        </Card>
      )}
      {purged ? <p role="status">{t('presence.purged')}</p> : null}
      {dialog === 'settings' && settings ? (
        <SettingsDialog
          settings={settings}
          onClose={() => setDialog(undefined)}
          onDone={() => {
            setDialog(undefined);
            loaded.reload();
            refresh();
          }}
        />
      ) : null}
      {dialog === 'purge' ? (
        <MutationDialog
          title={t('presence.purge')}
          danger
          onClose={() => setDialog(undefined)}
          submit={async () => {
            await presenceApi.purge();
            setDialog(undefined);
            setPurged(true);
          }}
        >
          <p>{t('presence.purgeConfirm')}</p>
        </MutationDialog>
      ) : null}
    </div>
  );
}
