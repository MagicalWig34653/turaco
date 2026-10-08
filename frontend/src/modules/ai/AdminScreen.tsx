import { useRef, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { DataTable } from '../../platform/ui/DataTable';
import { Dialog } from '../../platform/ui/Dialog';
import { useAi } from './AiProvider';
import { AiError } from './AiError';
import { aiApi } from './api';
import {
  dataClasses,
  type Provider,
  type ProviderFields,
  type Settings,
  type SettingsFields,
} from './types';
const limits: {
  key: Exclude<keyof SettingsFields, 'enabled' | 'retainConversations'>;
  min: number;
  max?: number;
}[] = [
  { key: 'retentionDays', min: 1, max: 30 },
  { key: 'userRequestsPerHour', min: 1, max: 10000 },
  { key: 'userRequestsPerDay', min: 1, max: 100000 },
  { key: 'userTokensPerDay', min: 1000 },
  { key: 'installationTokensPerDay', min: 1000 },
  { key: 'maxOutputTokens', min: 64, max: 8192 },
  { key: 'maxToolIterations', min: 1, max: 10 },
];
export function AdminScreen() {
  const { can } = useAi();
  const { t } = useI18n();
  if (!can('ai.admin')) return <p>{t('ai.unavailable')}</p>;
  return (
    <div className="ai-admin">
      <PageHeader title={t('ai.admin')} />
      {can('ai.settings.view') && <Configuration />}
      {can('ai.usage.view') && <Usage />}
    </div>
  );
}
function Configuration() {
  const { can, refresh } = useAi();
  const { t } = useI18n();
  const settings = useAsync(aiApi.settings, []);
  const providers = useAsync(aiApi.providers, []);
  const [editing, setEditing] = useState<Provider | 'new'>();
  const [error, setError] = useState<unknown>();
  const [result, setResult] = useState<{ id: string; ok: boolean; durationMs: number }>();
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const reload = () => {
    settings.reload();
    providers.reload();
    refresh();
  };
  async function test(p: Provider) {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError(undefined);
    setResult(undefined);
    try {
      const value = await aiApi.test(p.id);
      setResult({ id: p.id, ...value });
    } catch (e) {
      setError(e);
    } finally {
      lock.current = false;
      setBusy(false);
    }
  }
  return (
    <>
      <Button disabled={busy} onClick={reload}>
        {t('ai.reload')}
      </Button>
      <AiError error={settings.error ?? providers.error ?? error} />
      {settings.loading && <p role="status">{t('state.loading')}</p>}
      {settings.data && !settings.loading && (
        <SettingsForm
          key={settings.data.version}
          initial={settings.data}
          editable={can('ai.settings.manage')}
          onSaved={reload}
        />
      )}
      <h2>{t('ai.providers')}</h2>
      {can('ai.settings.manage') && (
        <Button onClick={() => setEditing('new')}>{t('ai.createProvider')}</Button>
      )}
      <DataTable
        caption={t('ai.providers')}
        emptyText={t('ai.none')}
        rows={providers.data?.items ?? []}
        rowKey={(p) => p.id}
        loading={providers.loading}
        columns={[
          { key: 'name', header: t('ai.field.displayName'), render: (p) => p.displayName },
          { key: 'model', header: t('ai.field.model'), render: (p) => p.model },
          {
            key: 'local',
            header: t('ai.location'),
            render: (p) => t(p.local ? 'ai.local' : 'ai.external'),
          },
          {
            key: 'enabled',
            header: t('ai.field.enabled'),
            render: (p) => t(p.enabled ? 'ai.yes' : 'ai.no'),
          },
          {
            key: 'actions',
            header: t('ai.actions'),
            render: (p) => (
              <>
                <Button onClick={() => setEditing(p)}>
                  {t(can('ai.settings.manage') ? 'ai.edit' : 'ai.view')}
                </Button>
                {can('ai.settings.manage') && (
                  <Button disabled={busy} onClick={() => void test(p)}>
                    {t('ai.test')}
                  </Button>
                )}
                {result?.id === p.id && (
                  <p role="status">
                    {t(result.ok ? 'ai.testOk' : 'ai.testFailed', { ms: result.durationMs })}
                  </p>
                )}
              </>
            ),
          },
        ]}
      />
      {editing && (
        <ProviderEditor
          original={editing === 'new' ? undefined : editing}
          editable={can('ai.settings.manage')}
          onClose={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            reload();
          }}
        />
      )}
    </>
  );
}
function SettingsForm({
  initial,
  editable,
  onSaved,
}: {
  initial: Settings;
  editable: boolean;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(initial);
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (lock.current || !editable) return;
        lock.current = true;
        setBusy(true);
        setError(undefined);
        void aiApi
          .saveSettings(draft)
          .then(onSaved, setError)
          .finally(() => {
            lock.current = false;
            setBusy(false);
          });
      }}
    >
      <h2>{t('ai.settings')}</h2>
      <AiError error={error} />
      <fieldset disabled={!editable || busy}>
        {(['enabled', 'retainConversations'] as const).map((key) => (
          <label key={key}>
            <span>{t(`ai.field.${key}`)}</span>
            <input
              type="checkbox"
              checked={draft[key]}
              onChange={(e) => setDraft({ ...draft, [key]: e.target.checked })}
            />
          </label>
        ))}
        {limits.map(({ key, min, max }) => (
          <label key={key}>
            {t(`ai.field.${key}`)}
            <input
              type="number"
              min={min}
              max={max}
              step={1}
              required
              value={draft[key]}
              onChange={(e) => setDraft({ ...draft, [key]: e.target.valueAsNumber })}
            />
          </label>
        ))}
        {editable && (
          <Button type="submit" busy={busy}>
            {t('ai.save')}
          </Button>
        )}
      </fieldset>
    </form>
  );
}
const emptyProvider: ProviderFields = {
  kind: 'openai_compatible',
  displayName: '',
  endpointUrl: '',
  model: '',
  local: false,
  allowedDataClasses: ['public_reference'],
  dpaRecordedOn: null,
  noTrainingConfirmed: false,
  region: '',
  secretRef: null,
  enabled: false,
  priceInPerMTok: 0,
  priceOutPerMTok: 0,
};
function ProviderEditor({
  original,
  editable,
  onClose,
  onSaved,
}: {
  original?: Provider | undefined;
  editable: boolean;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<ProviderFields>(original ?? emptyProvider);
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  return (
    <Dialog
      title={t(original ? 'ai.editProvider' : 'ai.createProvider')}
      onClose={() => {
        if (!busy) onClose();
      }}
    >
      <form
        className="ai-provider-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (lock.current || !editable) return;
          lock.current = true;
          setBusy(true);
          setError(undefined);
          void aiApi
            .saveProvider(draft, original)
            .then(onSaved, setError)
            .finally(() => {
              lock.current = false;
              setBusy(false);
            });
        }}
      >
        <AiError error={error} />
        <fieldset disabled={!editable || busy}>
          <label>
            {t('ai.field.kind')}
            <select
              value={draft.kind}
              disabled={!!original}
              onChange={(e) =>
                setDraft({ ...draft, kind: e.target.value as ProviderFields['kind'] })
              }
            >
              <option value="openai_compatible">{t('ai.kind.compatible')}</option>
              <option value="fake">{t('ai.kind.fake')}</option>
            </select>
          </label>
          {(
            ['displayName', 'endpointUrl', 'model', 'region', 'secretRef', 'dpaRecordedOn'] as const
          ).map((key) => (
            <label key={key}>
              {t(`ai.field.${key}`)}
              <input
                type={key === 'dpaRecordedOn' ? 'date' : key === 'endpointUrl' ? 'url' : 'text'}
                maxLength={key === 'displayName' ? 100 : undefined}
                required={
                  key === 'displayName' ||
                  (key === 'endpointUrl' && draft.kind !== 'fake') ||
                  ((key === 'region' || key === 'dpaRecordedOn') && !draft.local)
                }
                value={draft[key] ?? ''}
                onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
              />
            </label>
          ))}
          <p>{t('ai.secretNotice')}</p>
          {(['local', 'enabled', 'noTrainingConfirmed'] as const).map((key) => (
            <label key={key}>
              <span>{t(`ai.field.${key}`)}</span>
              <input
                type="checkbox"
                required={key === 'noTrainingConfirmed' && !draft.local}
                checked={draft[key]}
                onChange={(e) => setDraft({ ...draft, [key]: e.target.checked })}
              />
            </label>
          ))}
          <fieldset>
            <legend>{t('ai.allowedClasses')}</legend>
            {dataClasses.map((c) => (
              <label key={c}>
                <span>{t(`ai.class.${c}`)}</span>
                <input
                  type="checkbox"
                  checked={draft.allowedDataClasses.includes(c)}
                  onChange={(e) =>
                    setDraft({
                      ...draft,
                      allowedDataClasses: e.target.checked
                        ? [...draft.allowedDataClasses, c]
                        : draft.allowedDataClasses.filter((value) => value !== c),
                    })
                  }
                />
              </label>
            ))}
          </fieldset>
          {(['priceInPerMTok', 'priceOutPerMTok'] as const).map((key) => (
            <label key={key}>
              {t(`ai.field.${key}`)}
              <input
                type="number"
                min={0}
                step="any"
                required
                value={draft[key]}
                onChange={(e) => setDraft({ ...draft, [key]: e.target.valueAsNumber })}
              />
            </label>
          ))}
          {editable && (
            <Button type="submit" busy={busy}>
              {t('ai.save')}
            </Button>
          )}
        </fieldset>
        <Button disabled={busy} onClick={onClose}>
          {t('action.close')}
        </Button>
      </form>
    </Dialog>
  );
}
function Usage() {
  const { t } = useI18n();
  const usage = useAsync(aiApi.usage, []);
  return (
    <section>
      <h2>{t('ai.usage')}</h2>
      <p>{t('ai.costNotice')}</p>
      <AiError error={usage.error} />
      <Button onClick={usage.reload}>{t('ai.reload')}</Button>
      <DataTable
        caption={t('ai.usage')}
        emptyText={t('ai.none')}
        rows={usage.data?.items ?? []}
        rowKey={(u) => u.day}
        loading={usage.loading}
        columns={[
          { key: 'day', header: t('ai.day'), render: (u) => <time dateTime={u.day}>{u.day}</time> },
          { key: 'users', header: t('ai.users'), render: (u) => u.users },
          { key: 'requests', header: t('ai.requests'), render: (u) => u.requests },
          { key: 'in', header: t('ai.tokensIn'), render: (u) => u.tokensIn },
          { key: 'out', header: t('ai.tokensOut'), render: (u) => u.tokensOut },
          { key: 'cost', header: t('ai.cost'), render: (u) => u.estimatedCostMicro / 1000000 },
        ]}
      />
    </section>
  );
}
