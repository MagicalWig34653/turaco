import { useEffect, useState } from 'react';
import { api, ApiError } from '../../platform/api/client';
import { registerErrorMessages } from '../../platform/api/errorMessages';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { useSession } from '../../platform/session/SessionProvider';

type Target = { priority: string; kind: string; targetMinutes: number; timeBasis: string };
type Policy = {
  id: string;
  key: string;
  name: string;
  description: string;
  calendarId: string | null;
  pauseWaitingReasons: string[];
  warningPercent: number;
  escalationTask: boolean;
  status: string;
  version: number;
  targets: Target[];
};
type Calendar = { id: string; name: string; status: string };
type Queue = {
  id: string;
  key: string;
  name: string;
  status: string;
  version: number;
  policyId: string | null;
};
const priorities = ['low', 'normal', 'high', 'urgent'];
const kinds = ['first_response', 'resolution'];
const reasons = ['customer', 'vendor', 'external_service', 'scheduled_change', 'hardware'];
const blank = (): Policy => ({
  id: '',
  key: '',
  name: '',
  description: '',
  calendarId: null,
  pauseWaitingReasons: ['customer'],
  warningPercent: 80,
  escalationTask: false,
  status: 'active',
  version: 0,
  targets: [],
});
registerErrorMessages({
  'sla.invalid_request': 'sla.error.invalid',
  'sla.version_conflict': 'sla.error.conflict',
  'sla.policy_in_use': 'sla.error.inUse',
  'sla.key_taken': 'sla.error.keyTaken',
  'sla.not_found': 'sla.error.notFound',
  'calendars.in_use': 'calendars.error.inUse',
});
export function PoliciesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const manage = can('sla.manage');
  const [items, setItems] = useState<Policy[]>([]);
  const [calendars, setCalendars] = useState<Calendar[]>([]);
  const [queues, setQueues] = useState<Queue[]>([]);
  const [selected, setSelected] = useState<Policy>(blank());
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState(false);
  const reload = async () => {
    try {
      const [policies, queueData] = await Promise.all([
        api.get<{ items: Policy[] }>('/sla/policies'),
        api.get<{ items: Queue[] }>('/sla/queues'),
      ]);
      setItems(policies.items);
      setQueues(queueData.items);
    } catch (cause) {
      if (cause instanceof ApiError) setError(cause);
    }
  };
  useEffect(() => {
    void reload();
    void api
      .get<{ items: Calendar[] }>('/business-calendars')
      .then((data) => setCalendars(data.items))
      .catch((cause: unknown) => {
        if (cause instanceof ApiError) setError(cause);
      });
  }, []);
  const load = async (id: string) => {
    try {
      setSelected(await api.get<Policy>(`/sla/policies/${id}`));
      setError(undefined);
    } catch (cause) {
      if (cause instanceof ApiError) setError(cause);
    }
  };
  const perform = async (run: () => Promise<Policy>) => {
    setBusy(true);
    setError(undefined);
    try {
      const p = await run();
      await reload();
      await load(p.id);
    } catch (cause) {
      if (cause instanceof ApiError) setError(cause);
    } finally {
      setBusy(false);
    }
  };
  const save = () =>
    void perform(() => {
      const payload = {
        key: selected.key,
        name: selected.name,
        description: selected.description,
        calendarId: selected.calendarId,
        pauseWaitingReasons: selected.pauseWaitingReasons,
        warningPercent: selected.warningPercent,
        escalationTask: selected.escalationTask,
      };
      return selected.id
        ? api.patch<Policy>(`/sla/policies/${selected.id}`, {
            ...payload,
            expectedVersion: selected.version,
          })
        : api.post<Policy>('/sla/policies', payload);
    });
  const updateTarget = (
    priority: string,
    kind: string,
    part: 'targetMinutes' | 'timeBasis',
    value: string,
  ) => {
    const prior = selected.targets.find((v) => v.priority === priority && v.kind === kind);
    const next = {
      priority,
      kind,
      targetMinutes: prior?.targetMinutes ?? 60,
      timeBasis: prior?.timeBasis ?? 'business',
      [part]: part === 'targetMinutes' ? Number(value) : value,
    };
    setSelected((p) => ({
      ...p,
      targets: [...p.targets.filter((v) => v.priority !== priority || v.kind !== kind), next],
    }));
  };
  const removeTarget = (priority: string, kind: string) =>
    setSelected((p) => ({
      ...p,
      targets: p.targets.filter((v) => v.priority !== priority || v.kind !== kind),
    }));
  const setQueue = async (queue: Queue, policyId: string | null) => {
    setBusy(true);
    setError(undefined);
    try {
      await api.put(`/sla/queues/${queue.id}/policy`, {
        policyId,
        expectedQueueVersion: queue.version,
      });
      await reload();
    } catch (cause) {
      if (cause instanceof ApiError) setError(cause);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main>
      <PageHeader title={t('sla.title')} intro={t('sla.intro')} />
      {error && <ApiErrorAlert error={error} onRetry={reload} />}
      <section>
        <h2>{t('sla.policies')}</h2>
        {manage && <Button onClick={() => setSelected(blank())}>{t('sla.new')}</Button>}
        <ul>
          {items.map((p) => (
            <li key={p.id}>
              <Button onClick={() => void load(p.id)}>
                {p.name} ({p.key})
              </Button>
              {p.status === 'archived' ? ` — ${t('sla.archived')}` : ''}
            </li>
          ))}
        </ul>
      </section>
      <section>
        <h2>{selected.id ? t('sla.detail') : t('sla.new')}</h2>
        <label>
          {t('sla.key')}
          <input
            value={selected.key}
            disabled={!!selected.id || busy || !manage}
            onChange={(e) => setSelected({ ...selected, key: e.target.value })}
          />
        </label>
        <label>
          {t('sla.name')}
          <input
            value={selected.name}
            maxLength={80}
            disabled={busy || !manage}
            onChange={(e) => setSelected({ ...selected, name: e.target.value })}
          />
        </label>
        <label>
          {t('sla.description')}
          <textarea
            value={selected.description}
            maxLength={2000}
            disabled={busy || !manage}
            onChange={(e) => setSelected({ ...selected, description: e.target.value })}
          />
        </label>
        <label>
          {t('sla.calendar')}
          <select
            value={selected.calendarId ?? ''}
            disabled={busy || !manage}
            onChange={(e) => setSelected({ ...selected, calendarId: e.target.value || null })}
          >
            <option value="">{t('sla.none')}</option>
            {calendars
              .filter((c) => c.status === 'active' || c.id === selected.calendarId)
              .map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
          </select>
        </label>
        <fieldset>
          <legend>{t('sla.pauseReasons')}</legend>
          {reasons.map((reason) => (
            <label key={reason}>
              <input
                type="checkbox"
                checked={selected.pauseWaitingReasons.includes(reason)}
                disabled={busy || !manage}
                onChange={(e) =>
                  setSelected({
                    ...selected,
                    pauseWaitingReasons: e.target.checked
                      ? [...selected.pauseWaitingReasons, reason]
                      : selected.pauseWaitingReasons.filter((r) => r !== reason),
                  })
                }
              />
              {t(`sla.reason.${reason}` as Parameters<typeof t>[0])}
            </label>
          ))}
        </fieldset>
        <label>
          {t('sla.warningPercent')}
          <input
            type="number"
            min={50}
            max={95}
            value={selected.warningPercent}
            disabled={busy || !manage}
            onChange={(e) => setSelected({ ...selected, warningPercent: Number(e.target.value) })}
          />
        </label>
        <label>
          <input
            type="checkbox"
            checked={selected.escalationTask}
            disabled={busy || !manage}
            onChange={(e) => setSelected({ ...selected, escalationTask: e.target.checked })}
          />
          {t('sla.escalationTask')}
        </label>
        {manage && (
          <Button disabled={busy || selected.status !== 'active'} onClick={save}>
            {t('sla.save')}
          </Button>
        )}
        {manage && selected.id && (
          <Button
            disabled={busy || !manage}
            onClick={() =>
              void perform(() =>
                api.post<Policy>(
                  `/sla/policies/${selected.id}/${selected.status === 'active' ? 'archive' : 'restore'}`,
                  { expectedVersion: selected.version },
                ),
              )
            }
          >
            {selected.status === 'active' ? t('sla.archive') : t('sla.restore')}
          </Button>
        )}
      </section>
      {selected.id && (
        <section>
          <h2>{t('sla.targets')}</h2>
          <table>
            <thead>
              <tr>
                <th>{t('sla.priority')}</th>
                {kinds.map((k) => (
                  <th key={k}>{t(`sla.kind.${k}` as Parameters<typeof t>[0])}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {priorities.map((priority) => (
                <tr key={priority}>
                  <th>{t(`sla.priority.${priority}` as Parameters<typeof t>[0])}</th>
                  {kinds.map((kind) => {
                    const target = selected.targets.find(
                      (v) => v.priority === priority && v.kind === kind,
                    );
                    return (
                      <td key={kind}>
                        <label>
                          {t('sla.minutes')}
                          <input
                            type="number"
                            min={1}
                            max={129600}
                            value={target?.targetMinutes ?? ''}
                            disabled={busy || !manage || selected.status !== 'active'}
                            onChange={(e) =>
                              updateTarget(priority, kind, 'targetMinutes', e.target.value)
                            }
                          />
                        </label>
                        <select
                          aria-label={t('sla.timeBasis')}
                          value={target?.timeBasis ?? 'business'}
                          disabled={busy || !manage || selected.status !== 'active'}
                          onChange={(e) =>
                            updateTarget(priority, kind, 'timeBasis', e.target.value)
                          }
                        >
                          <option value="business">{t('sla.business')}</option>
                          <option value="continuous">{t('sla.continuous')}</option>
                        </select>
                        {manage && target && (
                          <Button disabled={busy} onClick={() => removeTarget(priority, kind)}>
                            {t('sla.removeTarget')}
                          </Button>
                        )}
                      </td>
                    );
                  })}
                </tr>
              ))}
            </tbody>
          </table>
          {manage && (
            <Button
              disabled={busy || selected.status !== 'active'}
              onClick={() =>
                void perform(() =>
                  api.put<Policy>(`/sla/policies/${selected.id}/targets`, {
                    expectedVersion: selected.version,
                    targets: selected.targets,
                  }),
                )
              }
            >
              {t('sla.saveTargets')}
            </Button>
          )}
        </section>
      )}
      <section>
        <h2>{t('sla.queues')}</h2>
        <table>
          <thead>
            <tr>
              <th>{t('sla.queue')}</th>
              <th>{t('sla.policy')}</th>
            </tr>
          </thead>
          <tbody>
            {queues
              .filter((q) => q.status === 'active')
              .map((q) => (
                <tr key={q.id}>
                  <td>
                    {q.name} ({q.key})
                  </td>
                  <td>
                    <select
                      aria-label={`${t('sla.policy')}: ${q.name}`}
                      value={q.policyId ?? ''}
                      disabled={busy || !manage}
                      onChange={(e) => void setQueue(q, e.target.value || null)}
                    >
                      <option value="">{t('sla.none')}</option>
                      {items
                        .filter((p) => p.status === 'active' || p.id === q.policyId)
                        .map((p) => (
                          <option key={p.id} value={p.id}>
                            {p.name}
                          </option>
                        ))}
                    </select>
                  </td>
                </tr>
              ))}
          </tbody>
        </table>
      </section>
    </main>
  );
}
