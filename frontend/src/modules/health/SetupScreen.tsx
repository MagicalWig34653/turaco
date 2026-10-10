import { useState } from 'react';
import { ApiError } from '../../platform/api/client';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ReasonDialog } from '../../platform/ui/ReasonDialog';
import { TableDate } from '../../platform/ui/TableDate';
import { Skeleton } from '../../platform/ui/Workspace';
import { healthApi } from './api';
import {
  expectedVersion,
  messageOr,
  orderedItems,
  resolveAppRoute,
  setupActions,
  setupTone,
  settledCount,
} from './model';
import type { SetupItem } from './types';
import './health.css';

type Pending = { item: SetupItem; action: 'skip' } | undefined;

function ItemRow({
  item,
  index,
  canWrite,
  busyKey,
  onSkip,
  onWrite,
}: {
  item: SetupItem;
  index: number;
  canWrite: boolean;
  busyKey: string | undefined;
  onSkip: (item: SetupItem) => void;
  onWrite: (item: SetupItem, state: 'confirmed' | 'cleared') => void;
}) {
  const { t } = useI18n();
  const route = resolveAppRoute(item.route);
  const actions = setupActions(item, canWrite);
  const busy = busyKey === item.key;
  return (
    <li className="setup-item" data-state={item.state}>
      <span className="setup-item-index" aria-hidden="true">
        {index + 1}
      </span>
      <div>
        <h3>{t(messageOr(`setup.item.${item.key}`, 'setup.item.unknown'), { key: item.key })}</h3>
        <p className="health-meta">
          {t(messageOr(`setup.item.${item.key}.hint`, 'setup.item.unknown.hint'))}
        </p>
        <p>
          <Badge tone={setupTone(item)}>{t(`setup.state.${item.state}`)}</Badge>
          {item.attention ? <Badge tone="warning">{t('setup.attention')}</Badge> : null}
        </p>
        {item.state === 'skipped' && item.reason ? (
          <p className="health-meta">{t('setup.skippedReason', { reason: item.reason })}</p>
        ) : null}
        {item.updatedAt && (item.state === 'skipped' || item.state === 'confirmed') ? (
          <p className="health-meta">
            {t('setup.markedAt')} <TableDate value={item.updatedAt} />
          </p>
        ) : null}
        <div className="setup-item-actions">
          {route ? (
            <Link
              to={route}
              className={`btn ${item.state === 'todo' ? 'btn-primary' : 'btn-secondary'}`}
            >
              {item.state === 'todo' ? t('setup.fix') : t('setup.open')}
            </Link>
          ) : null}
          {actions.skip ? (
            <Button onClick={() => onSkip(item)} disabled={busy}>
              {t('setup.skip')}
            </Button>
          ) : null}
          {actions.confirm ? (
            <Button variant="primary" busy={busy} onClick={() => onWrite(item, 'confirmed')}>
              {t('setup.confirm')}
            </Button>
          ) : null}
          {actions.clear ? (
            <Button busy={busy} onClick={() => onWrite(item, 'cleared')}>
              {t('setup.clear')}
            </Button>
          ) : null}
        </div>
      </div>
    </li>
  );
}

export function SetupScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const setup = useAsync((signal) => healthApi.setup(signal), []);
  const [pending, setPending] = useState<Pending>(undefined);
  const [busyKey, setBusyKey] = useState<string | undefined>(undefined);
  const [writeError, setWriteError] = useState<ApiError | undefined>(undefined);
  const [conflict, setConflict] = useState(false);
  const canWrite = can('platform.admin');
  const list = setup.data;

  const write = async (
    item: SetupItem,
    state: 'skipped' | 'confirmed' | 'cleared',
    reason?: string,
  ) => {
    const version = expectedVersion(item);
    await healthApi.writeSetupItem(item.key, {
      state,
      ...(reason ? { reason } : {}),
      ...(version !== undefined ? { expectedVersion: version } : {}),
    });
    setConflict(false);
    setup.reload();
  };

  const onWrite = async (item: SetupItem, state: 'confirmed' | 'cleared') => {
    setBusyKey(item.key);
    setWriteError(undefined);
    setConflict(false);
    try {
      await write(item, state);
    } catch (cause) {
      handleFailure(cause);
    } finally {
      setBusyKey(undefined);
    }
  };

  /** A version conflict means someone else changed the item: reload and ask the person to review. */
  function handleFailure(cause: unknown) {
    if (cause instanceof ApiError && cause.code === 'health.version_conflict') {
      setConflict(true);
      setup.reload();
      return;
    }
    setWriteError(cause instanceof ApiError ? cause : undefined);
  }

  return (
    <div className="health-screen">
      <PageHeader
        title={t('setup.title')}
        intro={t('setup.intro')}
        actions={<Button onClick={setup.reload}>{t('action.refresh')}</Button>}
      />
      {setup.error ? <ApiErrorAlert error={setup.error} onRetry={setup.reload} /> : null}
      {setup.loading && !list ? <Skeleton lines={4} /> : null}
      {conflict ? <Alert kind="warning">{t('health.error.versionConflict')}</Alert> : null}
      {writeError ? <ApiErrorAlert error={writeError} /> : null}
      {!canWrite && list ? <p className="health-meta">{t('setup.readOnly')}</p> : null}
      {list ? (
        <>
          <p role="status">
            {list.open > 0
              ? t('setup.progress', { done: settledCount(list), total: list.total })
              : t('setup.complete')}
          </p>
          <ol className="setup-list" aria-label={t('setup.title')}>
            {orderedItems(list).map((item, index) => (
              <ItemRow
                key={item.key}
                item={item}
                index={index}
                canWrite={canWrite}
                busyKey={busyKey}
                onSkip={(target) => setPending({ item: target, action: 'skip' })}
                onWrite={(target, state) => void onWrite(target, state)}
              />
            ))}
          </ol>
        </>
      ) : null}
      {pending ? (
        <ReasonDialog
          title={t('setup.skip.title')}
          label={t('setup.skip.reason')}
          hint={t('setup.skip.hint')}
          confirmLabel={t('setup.skip')}
          onSubmit={async (reason) => {
            try {
              await write(pending.item, 'skipped', reason);
              setPending(undefined);
            } catch (cause) {
              if (cause instanceof ApiError && cause.code === 'health.version_conflict') {
                setPending(undefined);
                handleFailure(cause);
                return;
              }
              throw cause;
            }
          }}
          onClose={() => setPending(undefined)}
        />
      ) : null}
    </div>
  );
}
