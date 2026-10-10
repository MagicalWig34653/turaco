import { useState } from 'react';
import { ApiError } from '../../platform/api/client';
import { useAsync } from '../../platform/api/useAsync';
import { en } from '../../platform/i18n/messages.en';
import type { MessageKey } from '../../platform/i18n/messages.en';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { Skeleton } from '../../platform/ui/Workspace';
import { settingsApi } from './api';
import { groupByModule, inputBounds, isDirty, messageOr, parseDraft, toDraft } from './model';
import type { AdminSetting } from './types';
import './settings.css';

function SettingRow({
  setting,
  canWrite,
  onSaved,
  onConflict,
}: {
  setting: AdminSetting;
  canWrite: boolean;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState(toDraft(setting, setting.value));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const label = t(messageOr(`settings.key.${setting.key}`, 'settings.key.unknown'), {
    key: setting.key,
  });
  const hintKey = `settings.key.${setting.key}.hint`;
  const hint = Object.hasOwn(en, hintKey) ? t(hintKey as MessageKey) : undefined;
  const parsed = parseDraft(setting, draft);
  const dirty = isDirty(setting, draft);
  const bounds = inputBounds(setting);
  const disabled = !canWrite || busy;

  const save = async () => {
    if (!parsed.ok) return;
    setBusy(true);
    setError(undefined);
    try {
      await settingsApi.write(setting.key, parsed.value, setting.version);
      onSaved();
    } catch (cause) {
      if (cause instanceof ApiError && cause.code === 'settings.version_conflict') {
        onConflict();
      } else {
        setError(cause instanceof ApiError ? cause : undefined);
      }
    } finally {
      setBusy(false);
    }
  };

  const options = (setting.options ?? []).map((value) => ({
    value,
    label: t(messageOr(`settings.option.${setting.key}.${value}`, 'settings.option.unknown'), {
      value,
    }),
  }));

  return (
    <li className="settings-row" data-key={setting.key}>
      {setting.type === 'bool' ? (
        <Checkbox
          label={label}
          description={hint}
          checked={draft === 'true'}
          disabled={disabled}
          onChange={(event) => setDraft(String(event.target.checked))}
        />
      ) : setting.type === 'enum' ? (
        <Select
          label={label}
          hint={hint}
          options={options}
          value={draft}
          disabled={disabled}
          onChange={(event) => setDraft(event.target.value)}
        />
      ) : (
        <TextField
          label={`${label} (${t(setting.type === 'duration' ? 'settings.unit.minutes' : 'settings.unit.count')})`}
          hint={hint}
          type="number"
          inputMode="numeric"
          step={1}
          {...bounds}
          value={draft}
          disabled={disabled}
          error={parsed.ok ? undefined : t('settings.error.outOfRange')}
          onChange={(event) => setDraft(event.target.value)}
        />
      )}
      <p className="settings-meta">
        {setting.notYetActive ? <Badge tone="info">{t('settings.notYetActive')}</Badge> : null}
        {setting.stored ? null : <Badge tone="neutral">{t('settings.default')}</Badge>}
        {setting.updatedAt ? (
          <span>
            {t('settings.changedAt')} <TableDate value={setting.updatedAt} />
          </span>
        ) : null}
      </p>
      {setting.sensitive && draft === 'true' && dirty ? (
        <Alert kind="warning">{t('settings.sensitiveWarning')}</Alert>
      ) : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {canWrite ? (
        <div className="settings-actions">
          <Button
            variant="primary"
            busy={busy}
            disabled={!dirty || !parsed.ok}
            onClick={() => void save()}
          >
            {t('action.save')}
          </Button>
          {dirty ? (
            <Button disabled={busy} onClick={() => setDraft(toDraft(setting, setting.value))}>
              {t('settings.reset')}
            </Button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}

export function SettingsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [conflict, setConflict] = useState(false);
  // Bumping the generation remounts rows so drafts follow the freshly loaded values.
  const [generation, setGeneration] = useState(0);
  const list = useAsync((signal) => settingsApi.list(signal), []);
  const canWrite = can('platform.admin');
  const reload = () => {
    list.reload();
    setGeneration((n) => n + 1);
  };

  return (
    <div className="settings-screen">
      <PageHeader
        title={t('settings.title')}
        intro={t('settings.intro')}
        actions={<Button onClick={reload}>{t('action.refresh')}</Button>}
      />
      {list.error ? <ApiErrorAlert error={list.error} onRetry={list.reload} /> : null}
      {list.loading && !list.data ? <Skeleton lines={4} /> : null}
      {conflict ? <Alert kind="warning">{t('settings.error.versionConflict')}</Alert> : null}
      {!canWrite && list.data ? <p className="settings-meta">{t('settings.readOnly')}</p> : null}
      {list.data
        ? groupByModule(list.data.items).map((group) => (
            <section key={group.module} aria-labelledby={`settings-${group.module}`}>
              <h2 id={`settings-${group.module}`}>
                {t(messageOr(`settings.module.${group.module}`, 'settings.module.unknown'), {
                  module: group.module,
                })}
              </h2>
              <ul className="settings-list">
                {group.items.map((setting) => (
                  <SettingRow
                    key={`${setting.key}:${setting.version}:${generation}`}
                    setting={setting}
                    canWrite={canWrite}
                    onSaved={() => {
                      setConflict(false);
                      reload();
                    }}
                    onConflict={() => {
                      setConflict(true);
                      reload();
                    }}
                  />
                ))}
              </ul>
            </section>
          ))
        : null}
    </div>
  );
}
