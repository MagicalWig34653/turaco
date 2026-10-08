import { useRef, useState } from 'react';
import { ApiError } from '../api/client';
import { useAsync } from '../api/useAsync';
import { useI18n } from '../i18n/I18nProvider';
import { en } from '../i18n/messages.en';
import type { MessageKey } from '../i18n/i18n';
import { Link, navigate } from '../router/Router';
import { useAi } from '../../modules/ai/AiProvider';
import { Alert, Badge } from '../ui/Alert';
import { ApiErrorAlert } from '../ui/ApiErrorAlert';
import { Button } from '../ui/Button';
import { DataTable } from '../ui/DataTable';
import { Dialog } from '../ui/Dialog';
import { PageHeader } from '../ui/PageHeader';
import { FilterBar } from '../ui/FilterBar';
import { TextField } from '../ui/Field';
import { modulesApi } from './api';
import {
  categories,
  filterModules,
  reasonCodes,
  settingsPath,
  switchAction,
  type Module,
  type ReasonCode,
} from './model';
import { useModules } from './ModulesProvider';
import './modules.css';
export function moduleMessage(key: string, fallback: MessageKey): MessageKey {
  return Object.hasOwn(en, key) ? (key as MessageKey) : fallback;
}
export function ModulesScreen() {
  const { t } = useI18n();
  const { can } = useAi();
  const { refresh } = useModules();
  const list = useAsync(modulesApi.list, []);
  const [query, setQuery] = useState('');
  const [category, setCategory] = useState('');
  const [state, setState] = useState('');
  const [selected, setSelected] = useState<Module>();
  const name = (key: string) => t(moduleMessage(`modules.${key}.name`, 'modules.unknown'));
  const settings = (item: Module) =>
    can(`${item.key}.admin`) ? settingsPath(item.key) : undefined;
  const items = filterModules(
    list.data?.items ?? [],
    query,
    category,
    state,
    (item) => `${name(item.key)} ${t(moduleMessage(item.descriptionKey, 'modules.unknown'))}`,
  );
  return (
    <div className="modules-screen">
      <PageHeader title={t('modules.title')} intro={t('modules.intro')} />
      <FilterBar>
        <TextField
          label={t('modules.search')}
          type="search"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
        <label>
          {t('modules.categoryLabel')}
          <select value={category} onChange={(event) => setCategory(event.target.value)}>
            <option value="">{t('modules.all')}</option>
            {categories.map((key) => (
              <option key={key} value={key}>
                {t(`modules.category.${key}`)}
              </option>
            ))}
          </select>
        </label>
        <label>
          {t('modules.stateLabel')}
          <select value={state} onChange={(event) => setState(event.target.value)}>
            <option value="">{t('modules.all')}</option>
            {(['enabled', 'disabled', 'blocked'] as const).map((key) => (
              <option key={key} value={key}>
                {t(`modules.state.${key}`)}
              </option>
            ))}
          </select>
        </label>
        <Button
          onClick={() => {
            list.reload();
            refresh();
          }}
        >
          {t('action.retry')}
        </Button>
      </FilterBar>
      {list.error && <ApiErrorAlert error={list.error} onRetry={list.reload} />}
      {list.loading && <p role="status">{t('state.loading')}</p>}
      {!list.loading && !list.error && !items.length && <p>{t('modules.empty')}</p>}
      {categories.map((group) => {
        const rows = items.filter((item) => item.category === group);
        if (!rows.length) return null;
        return (
          <section key={group}>
            <h2>{t(`modules.category.${group}`)}</h2>
            <DataTable
              caption={t(`modules.category.${group}`)}
              rows={rows}
              rowKey={(item) => item.key}
              emptyText={t('modules.empty')}
              rowActions={(item) => [
                ...(!item.core
                  ? [
                      {
                        id: 'toggle',
                        label: t(`modules.${switchAction(item)}`),
                        onSelect: () => setSelected(item),
                      },
                    ]
                  : []),
                ...(settings(item)
                  ? [
                      {
                        id: 'settings',
                        label: t('modules.openSettings'),
                        onSelect: () => navigate(settings(item)!),
                      },
                    ]
                  : []),
              ]}
              columns={[
                {
                  key: 'name',
                  header: t('modules.name'),
                  render: (item) => (
                    <>
                      <strong>{name(item.key)}</strong>
                      <p>{t(moduleMessage(item.descriptionKey, 'modules.unknown'))}</p>
                      {item.core && (
                        <Badge>
                          <span aria-hidden="true">🔒</span> {t('modules.core')}
                        </Badge>
                      )}
                    </>
                  ),
                },
                {
                  key: 'state',
                  header: t('modules.stateLabel'),
                  render: (item) => (
                    <>
                      <Badge
                        tone={
                          item.state === 'enabled'
                            ? 'success'
                            : item.state === 'blocked'
                              ? 'warning'
                              : 'neutral'
                        }
                      >
                        {t(`modules.state.${item.state}`)}
                      </Badge>
                      {item.blockedReason && <p>{t(`modules.blocked.${item.blockedReason}`)}</p>}
                      {settings(item) && (
                        <Link to={settings(item)!}>{t('modules.openSettings')}</Link>
                      )}
                    </>
                  ),
                },
                ...(['requires', 'requiredBy'] as const).map((key) => ({
                  key,
                  header: t(`modules.${key}`),
                  render: (item: Module) => (
                    <div className="module-chips">
                      {item[key].length
                        ? item[key].map((dependency) => (
                            <Badge key={dependency}>{name(dependency)}</Badge>
                          ))
                        : t('modules.none')}
                    </div>
                  ),
                })),
                {
                  key: 'gates',
                  header: t('modules.startupGates'),
                  render: (item) => (
                    <>
                      {item.startupGates.length ? (
                        <>
                          <p>{t('modules.startupInfo')}</p>
                          {item.startupGates.map((gate) => (
                            <code key={gate}>{gate}</code>
                          ))}
                        </>
                      ) : (
                        t('modules.none')
                      )}
                    </>
                  ),
                },
                {
                  key: 'action',
                  header: t('modules.action'),
                  render: (item) =>
                    item.core ? null : (
                      <Button onClick={() => setSelected(item)}>
                        {t(`modules.${switchAction(item)}`)}
                      </Button>
                    ),
                },
              ]}
            />
          </section>
        );
      })}
      {selected && (
        <ToggleDialog
          module={selected}
          onClose={() => setSelected(undefined)}
          onSaved={() => {
            setSelected(undefined);
            list.reload();
            refresh();
          }}
          onConflict={() => {
            list.reload();
            refresh();
          }}
        />
      )}
    </div>
  );
}
function ToggleDialog({
  module,
  onClose,
  onSaved,
  onConflict,
}: {
  module: Module;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState<ReasonCode>('initial_setup');
  const [error, setError] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  const [stale, setStale] = useState(false);
  const lock = useRef(false);
  const action = switchAction(module);
  return (
    <Dialog
      title={t(`modules.${action}`)}
      onClose={() => {
        if (!lock.current) onClose();
      }}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (lock.current || stale) return;
          lock.current = true;
          setBusy(true);
          setError(undefined);
          void modulesApi
            .toggle(module, reason)
            .then(onSaved, (cause: unknown) => {
              setError(cause);
              if (cause instanceof ApiError && cause.status === 409) {
                onConflict();
                setStale(true);
              }
            })
            .finally(() => {
              lock.current = false;
              setBusy(false);
            });
        }}
      >
        <p>{t('modules.confirm', { name: t(moduleMessage(module.nameKey, 'modules.unknown')) })}</p>
        <p>{t('modules.keepsData')}</p>
        {error instanceof ApiError ? (
          <Alert kind="error">
            <p>
              {t(
                moduleMessage(
                  `modules.error.${error.code.replace('platform.modules.', '')}`,
                  'error.generic',
                ),
              )}
            </p>
            {error.blockers.length > 0 && (
              <>
                <p>{t('modules.blockers')}</p>
                <ul>
                  {error.blockers.map((key) => (
                    <li key={key}>{t(moduleMessage(`modules.${key}.name`, 'modules.unknown'))}</li>
                  ))}
                </ul>
              </>
            )}
            {module.blockedReason && <p>{t(`modules.blocked.${module.blockedReason}`)}</p>}
            {stale && <p>{t('modules.reviewAgain')}</p>}
          </Alert>
        ) : error ? (
          <Alert kind="error">{t('error.generic')}</Alert>
        ) : null}
        <label>
          {t('modules.reason')}
          <select
            value={reason}
            disabled={busy}
            onChange={(event) => setReason(event.target.value as ReasonCode)}
          >
            {reasonCodes.map((key) => (
              <option key={key} value={key}>
                {t(`modules.reason.${key}`)}
              </option>
            ))}
          </select>
        </label>
        <div className="form-actions">
          <Button type="submit" variant="primary" busy={busy} disabled={stale}>
            {t(`modules.${action}`)}
          </Button>
          <Button disabled={busy} onClick={onClose}>
            {t('action.close')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
