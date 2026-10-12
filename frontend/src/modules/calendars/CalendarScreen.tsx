import { useEffect, useState } from 'react';
import { api, ApiError } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';

type Interval = { start: number; end: number };
type Closure = { date: string; name: string };
type Calendar = {
  id: string;
  key: string;
  name: string;
  timezone: string;
  weeklyHours: Record<string, Interval[]>;
  status: string;
  version: number;
  closures: Closure[];
};
registerErrorMessages({
  'calendars.invalid_request': 'calendars.error.invalid',
  'calendars.version_conflict': 'calendars.error.conflict',
  'calendars.key_taken': 'calendars.error.keyTaken',
  'calendars.not_found': 'calendars.error.notFound',
  'calendars.in_use': 'calendars.error.inUse',
});
const emptyHours = (): Record<string, Interval[]> => ({
  '1': [{ start: 540, end: 1020 }],
  '2': [{ start: 540, end: 1020 }],
  '3': [{ start: 540, end: 1020 }],
  '4': [{ start: 540, end: 1020 }],
  '5': [{ start: 540, end: 1020 }],
});

export function CalendarScreen() {
  const { t } = useI18n();
  const [items, setItems] = useState<Calendar[]>([]);
  const [selected, setSelected] = useState<Calendar>();
  const [key, setKey] = useState('');
  const [name, setName] = useState('');
  const [timezone, setTimezone] = useState('Europe/Berlin');
  const [hours, setHours] = useState<Record<string, Interval[]>>(emptyHours());
  const [year, setYear] = useState(new Date().getFullYear());
  const [closures, setClosures] = useState<Closure[]>([]);
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const reload = async () => {
    try {
      const data = await api.get<{ items: Calendar[] }>('/business-calendars');
      setItems(data.items);
    } catch (e) {
      if (e instanceof ApiError) setError(e);
    }
  };
  useEffect(() => {
    void reload();
  }, []);
  const load = async (id: string) => {
    try {
      const c = await api.get<Calendar>(`/business-calendars/${id}`);
      setSelected(c);
      setKey(c.key);
      setName(c.name);
      setTimezone(c.timezone);
      setHours(c.weeklyHours);
      setClosures(c.closures);
      setError(undefined);
    } catch (e) {
      if (e instanceof ApiError) setError(e);
    }
  };
  const startNew = () => {
    setSelected(undefined);
    setKey('');
    setName('');
    setTimezone('Europe/Berlin');
    setHours(emptyHours());
    setClosures([]);
    setError(undefined);
  };
  const save = async () => {
    setBusy(true);
    setError(undefined);
    try {
      const payload = {
        key,
        name,
        timezone,
        weeklyHours: hours,
        expectedVersion: selected?.version,
      };
      const c = selected
        ? await api.patch<Calendar>(`/business-calendars/${selected.id}`, payload)
        : await api.post<Calendar>('/business-calendars', payload);
      await reload();
      await load(c.id);
    } catch (e) {
      if (e instanceof ApiError) setError(e);
    } finally {
      setBusy(false);
    }
  };
  const saveClosures = async () => {
    if (!selected) return;
    setBusy(true);
    setError(undefined);
    try {
      const c = await api.put<Calendar>(`/business-calendars/${selected.id}/closures/${year}`, {
        expectedVersion: selected.version,
        items: closures.filter((x) => x.date.startsWith(String(year))),
      });
      await load(c.id);
    } catch (e) {
      if (e instanceof ApiError) setError(e);
    } finally {
      setBusy(false);
    }
  };
  const changeStatus = async () => {
    if (!selected) return;
    setBusy(true);
    setError(undefined);
    try {
      const op = selected.status === 'active' ? 'archive' : 'restore';
      const c = await api.post<Calendar>(`/business-calendars/${selected.id}/${op}`, {
        expectedVersion: selected.version,
      });
      await reload();
      await load(c.id);
    } catch (e) {
      if (e instanceof ApiError) setError(e);
    } finally {
      setBusy(false);
    }
  };
  const copyClosures = async () => {
    if (!selected) return;
    setBusy(true);
    setError(undefined);
    try {
      const c = await api.post<Calendar>(`/business-calendars/${selected.id}/copy-closures`, {
        expectedVersion: selected.version,
        fromYear: year,
        toYear: year + 1,
      });
      await load(c.id);
    } catch (e) {
      if (e instanceof ApiError) setError(e);
    } finally {
      setBusy(false);
    }
  };
  const setInterval = (day: string, index: number, part: 'start' | 'end', value: string) =>
    setHours((current) => ({
      ...current,
      [day]: (current[day] ?? []).map((interval, i) =>
        i === index ? { ...interval, [part]: Number(value) } : interval,
      ),
    }));
  return (
    <main>
      <PageHeader title={t('calendars.title')} intro={t('calendars.intro')} />
      {error && <ApiErrorAlert error={error} onRetry={reload} />}
      <section>
        <h2>{t('calendars.list')}</h2>
        <Button onClick={startNew}>{t('calendars.new')}</Button>
        <ul>
          {items.map((c) => (
            <li key={c.id}>
              <Button onClick={() => void load(c.id)}>
                {c.name} ({c.key})
              </Button>{' '}
              {c.status === 'archived' ? t('calendars.archived') : ''}
            </li>
          ))}
        </ul>
      </section>
      <section>
        <h2>{selected ? t('calendars.edit') : t('calendars.new')}</h2>
        <TextField
          label={t('calendars.key')}
          value={key}
          disabled={!!selected}
          onChange={(e) => setKey(e.target.value)}
        />
        <TextField
          label={t('calendars.name')}
          value={name}
          maxLength={80}
          onChange={(e) => setName(e.target.value)}
        />
        <TextField
          label={t('calendars.timezone')}
          value={timezone}
          onChange={(e) => setTimezone(e.target.value)}
        />
        <h3>{t('calendars.hours')}</h3>
        {Array.from({ length: 7 }, (_, i) => String(i + 1)).map((day) => (
          <div key={day}>
            <strong>{t(`calendars.day.${day}` as 'calendars.day.1')}</strong>
            {(hours[day] ?? []).map((interval, index) => (
              <span key={index}>
                <input
                  aria-label={`${t('calendars.start')} ${day}`}
                  type="number"
                  min={0}
                  max={1439}
                  value={interval.start}
                  onChange={(e) => setInterval(day, index, 'start', e.target.value)}
                />
                <input
                  aria-label={`${t('calendars.end')} ${day}`}
                  type="number"
                  min={1}
                  max={1440}
                  value={interval.end}
                  onChange={(e) => setInterval(day, index, 'end', e.target.value)}
                />
                <Button
                  onClick={() =>
                    setHours((current) => ({
                      ...current,
                      [day]: (current[day] ?? []).filter((_, i) => i !== index),
                    }))
                  }
                >
                  {t('calendars.remove')}
                </Button>
              </span>
            ))}
            <Button
              onClick={() =>
                setHours((current) => ({
                  ...current,
                  [day]: [...(current[day] ?? []), { start: 540, end: 1020 }],
                }))
              }
            >
              {t('calendars.addInterval')}
            </Button>
          </div>
        ))}
        <Button busy={busy} onClick={() => void save()}>
          {t('calendars.save')}
        </Button>
        {selected && (
          <Button busy={busy} onClick={() => void changeStatus()}>
            {selected.status === 'active' ? t('calendars.archive') : t('calendars.restore')}
          </Button>
        )}
      </section>
      {selected && (
        <section>
          <h3>{t('calendars.closures')}</h3>
          <TextField
            label={t('calendars.year')}
            type="number"
            value={year}
            onChange={(e) => setYear(Number(e.target.value))}
          />
          {closures
            .map((cl, index) => ({ cl, index }))
            .filter(({ cl }) => cl.date.startsWith(String(year)))
            .map(({ cl, index }) => (
              <div key={index}>
                <TextField
                  label={t('calendars.date')}
                  type="date"
                  value={cl.date}
                  onChange={(e) =>
                    setClosures((current) =>
                      current.map((x, i) => (i === index ? { ...x, date: e.target.value } : x)),
                    )
                  }
                />
                <TextField
                  label={t('calendars.closureName')}
                  value={cl.name}
                  onChange={(e) =>
                    setClosures((current) =>
                      current.map((x, i) => (i === index ? { ...x, name: e.target.value } : x)),
                    )
                  }
                />
                <Button
                  onClick={() => setClosures((current) => current.filter((_, i) => i !== index))}
                >
                  {t('calendars.remove')}
                </Button>
              </div>
            ))}
          <Button
            onClick={() =>
              setClosures((current) => [...current, { date: `${year}-01-01`, name: '' }])
            }
          >
            {t('calendars.addClosure')}
          </Button>
          <Button busy={busy} onClick={() => void saveClosures()}>
            {t('calendars.saveClosures')}
          </Button>
          <Button busy={busy} onClick={() => void copyClosures()}>
            {t('calendars.copyNextYear')}
          </Button>
        </section>
      )}
    </main>
  );
}
