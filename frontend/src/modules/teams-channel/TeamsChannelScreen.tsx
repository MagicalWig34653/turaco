import { useState } from 'react';
import { ApiError } from '../../platform/api/client';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/messages.en';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Table } from '../../platform/ui/Table';
import { TableDate } from '../../platform/ui/TableDate';
import { Skeleton } from '../../platform/ui/Workspace';
import { teamsChannelApi } from './api';
import { freeDestinations, routesFor } from './model';
import type { ChannelRoute } from './types';
import './teams-channel.css';

const categoryLabels: Record<string, MessageKey> = {
  'majorincident.update': 'teamsChannel.category.majorincident.update',
  'change.scheduled': 'teamsChannel.category.change.scheduled',
};

function categoryLabel(t: (key: MessageKey) => string, category: string): string {
  const key = categoryLabels[category];
  return key ? t(key) : category;
}

/**
 * Administration of the Teams channel routes (ADR-0036, T-A): which broadcastable category is posted to which
 * configured destination. Only destination keys are shown; the webhook URLs stay in the deployment secret file.
 * The backend authorizes (integrations.teams.manage); this screen only hides what the caller cannot use.
 */
export function TeamsChannelScreen() {
  const { t } = useI18n();
  const list = useAsync((signal) => teamsChannelApi.list(signal), []);
  const [category, setCategory] = useState('');
  const [destination, setDestination] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [removing, setRemoving] = useState<ChannelRoute | undefined>(undefined);
  const data = list.data;

  const add = async () => {
    if (!category || !destination) return;
    setBusy(true);
    setError(undefined);
    try {
      await teamsChannelApi.create(category, destination);
      setDestination('');
      list.reload();
    } catch (cause) {
      setError(cause instanceof ApiError ? cause : undefined);
    } finally {
      setBusy(false);
    }
  };

  const free = data && category ? freeDestinations(data, category) : [];

  return (
    <div className="teams-channel">
      <PageHeader
        title={t('teamsChannel.title')}
        intro={t('teamsChannel.intro')}
        actions={<Button onClick={list.reload}>{t('action.refresh')}</Button>}
      />
      {list.error ? <ApiErrorAlert error={list.error} onRetry={list.reload} /> : null}
      {list.loading && !data ? <Skeleton lines={4} /> : null}
      {data ? (
        <>
          <p className="teams-channel-meta">
            <Badge tone={data.mode === 'real' ? 'success' : 'warning'}>
              {t(`teamsChannel.mode.${data.mode}`)}
            </Badge>
          </p>
          {data.mode === 'not_configured' ? (
            <Alert kind="warning">{t('teamsChannel.notConfigured')}</Alert>
          ) : null}
          <Alert kind="info">{t('teamsChannel.referenceOnly')}</Alert>
          {data.categories.map((name) => (
            <section key={name} aria-labelledby={`teams-route-${name}`}>
              <h2 id={`teams-route-${name}`}>{categoryLabel(t, name)}</h2>
              {routesFor(data, name).length === 0 ? (
                <p>{t('teamsChannel.noRoutes')}</p>
              ) : (
                <Table aria-label={categoryLabel(t, name)}>
                  <thead>
                    <tr>
                      <th scope="col">{t('teamsChannel.destination')}</th>
                      <th scope="col">{t('teamsChannel.since')}</th>
                      <th scope="col">
                        <span className="visually-hidden">{t('teamsChannel.actions')}</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {routesFor(data, name).map((route) => (
                      <tr key={route.id}>
                        <td>
                          <code>{route.destinationKey}</code>
                          {data.destinations.includes(route.destinationKey) ? null : (
                            <Badge tone="warning">{t('teamsChannel.destinationMissing')}</Badge>
                          )}
                        </td>
                        <td>
                          <TableDate value={route.createdAt} />
                        </td>
                        <td>
                          <Button onClick={() => setRemoving(route)}>
                            {t('teamsChannel.remove')}
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              )}
            </section>
          ))}
          <section aria-labelledby="teams-route-add" className="teams-channel-add">
            <h2 id="teams-route-add">{t('teamsChannel.add')}</h2>
            <Select
              label={t('teamsChannel.category')}
              value={category}
              onChange={(event) => {
                setCategory(event.target.value);
                setDestination('');
              }}
              options={[
                { value: '', label: t('teamsChannel.choose') },
                ...data.categories.map((name) => ({ value: name, label: categoryLabel(t, name) })),
              ]}
            />
            <Select
              label={t('teamsChannel.destination')}
              value={destination}
              disabled={!category}
              onChange={(event) => setDestination(event.target.value)}
              options={[
                { value: '', label: t('teamsChannel.choose') },
                ...free.map((key) => ({ value: key, label: key })),
              ]}
            />
            {error ? <ApiErrorAlert error={error} /> : null}
            <div>
              <Button
                variant="primary"
                busy={busy}
                disabled={!category || !destination}
                onClick={() => void add()}
              >
                {t('teamsChannel.addRoute')}
              </Button>
            </div>
          </section>
        </>
      ) : null}
      {removing ? (
        <GuardedActionDialog
          title={t('teamsChannel.remove')}
          confirmLabel={t('teamsChannel.remove')}
          danger
          run={async () => {
            await teamsChannelApi.remove(removing.id);
          }}
          onDone={() => {
            setRemoving(undefined);
            list.reload();
          }}
          onClose={() => setRemoving(undefined)}
        >
          <p>
            {t('teamsChannel.removeConfirm', {
              category: categoryLabel(t, removing.category),
              destination: removing.destinationKey,
            })}
          </p>
        </GuardedActionDialog>
      ) : null}
    </div>
  );
}
