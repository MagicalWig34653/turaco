import { useRef, useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextField } from '../../platform/ui/Field';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { organizationApi } from '../organization/api';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { presenceApi } from './api';
import { dateInZone, entryBody, type EntryDraft } from './model';
import type { Entry, Operation } from './types';
export function LocationPicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (id: string) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [query, setQuery] = useState('');
  const search = useDebouncedValue(query, 250);
  const loaded = useAsync(
    async (signal) =>
      can('organization.view')
        ? (await organizationApi.searchLocations(search, signal)).items.filter(
            (item) => item.active,
          )
        : [],
    [search, can('organization.view')],
  );
  const selected = useAsync(
    async (signal) =>
      value && can('organization.view') ? organizationApi.location(value, signal) : null,
    [value, can('organization.view')],
  );
  return (
    <div>
      <TextField
        label={t('presence.locationSearch')}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        disabled={!can('organization.view')}
      />
      {loaded.error ? <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} /> : null}
      <Select
        label={t('presence.location')}
        required
        value={value}
        onChange={(e) => onChange(e.target.value)}
        options={[
          { value: '', label: t('presence.chooseLocation') },
          ...(value && !loaded.data?.some((item) => item.id === value)
            ? [
                {
                  value,
                  label:
                    selected.data?.id === value
                      ? selected.data.name
                      : t('presence.currentLocation'),
                },
              ]
            : []),
          ...(loaded.data ?? []).map((item) => ({ value: item.id, label: item.name })),
        ]}
      />
      {!can('organization.view') ? <p>{t('presence.directoryPermission')}</p> : null}
    </div>
  );
}
function localInput(value: string) {
  const date = new Date(value);
  return `${dateInZone(date)}T${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}
export function EntryDialog({
  operation,
  entry,
  onClose,
  onDone,
}: {
  operation: Operation;
  entry?: Entry | undefined;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const zone = entry?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone;
  const [draft, setDraft] = useState<EntryDraft>(() => ({
    kind: entry?.kind ?? 'work_location',
    locationType: entry?.locationType ?? 'remote',
    locationId: entry?.locationId ?? '',
    allDay: entry?.allDay ?? true,
    start: entry
      ? entry.allDay
        ? dateInZone(entry.startsAt, zone)
        : localInput(entry.startsAt)
      : dateInZone(new Date()),
    end: entry
      ? entry.allDay
        ? dateInZone(new Date(Date.parse(entry.endsAt) - 1), zone)
        : localInput(entry.endsAt)
      : dateInZone(new Date()),
    timezone: zone,
    visibility: entry?.visibility ?? 'availability',
    frequency: entry?.recurrence?.frequency ?? '',
    interval: entry?.recurrence?.interval ?? 1,
    endsOn: entry?.recurrence?.endsOn ?? '',
    weekday: entry?.recurrence?.weekday ?? 1,
    dayOfMonth: entry?.recurrence?.dayOfMonth ?? 1,
  }));
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const pending = useRef(false);
  const update = <K extends keyof EntryDraft>(key: K, value: EntryDraft[K]) =>
    setDraft((old) => ({ ...old, [key]: value }));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (pending.current) return;
    pending.current = true;
    setBusy(true);
    setError(undefined);
    try {
      await presenceApi.operate(operation, entry?.id, entryBody(draft, operation, entry));
      onDone();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      pending.current = false;
      setBusy(false);
    }
  };
  const times = operation === 'create' || operation === 'reschedule';
  const location =
    operation === 'change-location' || (operation === 'create' && draft.kind === 'work_location');
  const recurrence = operation === 'create' || operation === 'change-recurrence';
  return (
    <Dialog
      title={t(`presence.action.${operation}`)}
      onClose={() => {
        if (!pending.current) onClose();
      }}
    >
      <form className="form" onSubmit={(e) => void submit(e)}>
        <p className="field-hint">{t('presence.privacy')}</p>
        {error ? <ApiErrorAlert error={error} /> : null}
        <fieldset disabled={busy} className="presence-fields">
          {entry?.allDay && !entry.timezone && times ? <p>{t('presence.zoneUnknown')}</p> : null}
          {operation === 'cancel' ? <p>{t('presence.cancelConfirm')}</p> : null}
          {operation === 'create' ? (
            <Select
              label={t('presence.kind')}
              value={draft.kind}
              onChange={(e) => update('kind', e.target.value as EntryDraft['kind'])}
              options={(['work_location', 'unavailable'] as const).map((value) => ({
                value,
                label: t(`presence.kind.${value}`),
              }))}
            />
          ) : null}
          {location ? (
            <>
              <Select
                label={t('presence.locationType')}
                value={draft.locationType}
                onChange={(e) =>
                  update('locationType', e.target.value as EntryDraft['locationType'])
                }
                options={(['location', 'remote', 'travelling'] as const).map((value) => ({
                  value,
                  label: t(`presence.location.${value}`),
                }))}
              />
              {draft.locationType === 'location' ? (
                <LocationPicker
                  value={draft.locationId}
                  onChange={(id) => update('locationId', id)}
                />
              ) : null}
            </>
          ) : null}
          {times ? (
            <>
              <label>
                <input
                  type="checkbox"
                  checked={draft.allDay}
                  onChange={(e) =>
                    setDraft((old) => ({
                      ...old,
                      allDay: e.target.checked,
                      start: old.start.slice(0, 10) + (e.target.checked ? '' : 'T09:00'),
                      end: old.end.slice(0, 10) + (e.target.checked ? '' : 'T17:00'),
                    }))
                  }
                />{' '}
                {t('presence.allDay')}
              </label>
              <TextField
                label={t('presence.start')}
                type={draft.allDay ? 'date' : 'datetime-local'}
                required
                value={draft.start}
                onChange={(e) => update('start', e.target.value)}
              />
              <TextField
                label={t('presence.end')}
                type={draft.allDay ? 'date' : 'datetime-local'}
                required
                min={draft.start}
                value={draft.end}
                onChange={(e) => update('end', e.target.value)}
              />
              {!draft.allDay ? (
                <p>
                  {t('presence.localTimes', {
                    zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
                  })}
                </p>
              ) : null}
            </>
          ) : null}
          {times || recurrence ? (
            <TextField
              label={t('presence.timezone')}
              required
              value={draft.timezone}
              onChange={(e) => update('timezone', e.target.value)}
            />
          ) : null}
          {recurrence ? (
            <>
              <Select
                label={t('presence.recurrence')}
                value={draft.frequency}
                onChange={(e) => update('frequency', e.target.value as EntryDraft['frequency'])}
                options={(['', 'daily', 'weekly', 'monthly'] as const).map((value) => ({
                  value,
                  label: t(`presence.repeat.${value || 'none'}`),
                }))}
              />
              {draft.frequency ? (
                <>
                  <TextField
                    label={t('presence.interval')}
                    type="number"
                    min={1}
                    max={365}
                    required
                    value={draft.interval}
                    onChange={(e) => update('interval', Number(e.target.value))}
                  />
                  {draft.frequency === 'weekly' ? (
                    <Select
                      label={t('presence.weekday')}
                      value={String(draft.weekday)}
                      onChange={(e) => update('weekday', Number(e.target.value))}
                      options={Array.from({ length: 7 }, (_, i) => ({
                        value: String(i + 1),
                        label: t(`presence.weekday.${i + 1}` as 'presence.weekday.1'),
                      }))}
                    />
                  ) : null}
                  {draft.frequency === 'monthly' ? (
                    <TextField
                      label={t('presence.dayOfMonth')}
                      type="number"
                      min={1}
                      max={31}
                      required
                      value={draft.dayOfMonth}
                      onChange={(e) => update('dayOfMonth', Number(e.target.value))}
                    />
                  ) : null}
                  <TextField
                    label={t('presence.endsOn')}
                    type="date"
                    required
                    value={draft.endsOn}
                    onChange={(e) => update('endsOn', e.target.value)}
                  />
                  <p className="field-hint">{t('presence.recurrenceHint')}</p>
                </>
              ) : null}
            </>
          ) : null}
          {operation === 'create' ? (
            <Select
              label={t('presence.visibility')}
              value={draft.visibility}
              onChange={(e) => update('visibility', e.target.value as EntryDraft['visibility'])}
              options={(['availability', 'detail'] as const).map((value) => ({
                value,
                label: t(`presence.visibility.${value}`),
              }))}
            />
          ) : null}
        </fieldset>
        <div className="dialog-actions">
          <Button disabled={busy} onClick={onClose}>
            {t('action.cancel')}
          </Button>
          <Button type="submit" busy={busy} variant={operation === 'cancel' ? 'danger' : 'primary'}>
            {t(`presence.action.${operation}`)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
