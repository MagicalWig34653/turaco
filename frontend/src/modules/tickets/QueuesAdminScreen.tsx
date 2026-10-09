import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import type { MenuItem } from '../../platform/ui/ContextMenu';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { Checkbox } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { useReferenceNames } from '../../platform/ui/query/referenceNames';
import { notifySidebarChanged } from '../../platform/ui/views/api';
import { Toast } from '../../platform/ui/Workspace';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { ticketQueuesApi } from './api';
import { QueueGrantsDialog } from './QueueGrantsDialog';
import { QueueCreateDialog, QueueEditDialog } from './QueueDialogs';
import type { TicketQueue } from './types';
import './queues.css';

type Action = 'archive' | 'restore' | 'makeDefault';
type Dialogs =
  | { kind: 'create' }
  | { kind: 'edit'; queue: TicketQueue }
  | { kind: 'grants'; queue: TicketQueue }
  | { kind: 'confirm'; action: Action; queue: TicketQueue };

/** Service Desk queue administration (needs servicedesk.queues.manage; the server enforces it). */
export function QueuesAdminScreen() {
  const { t } = useI18n();
  const list = useAsync((signal) => ticketQueuesApi.list(false, signal), []);
  const [showArchived, setShowArchived] = useState(false);
  const [dialog, setDialog] = useState<Dialogs | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmError, setConfirmError] = useState<ApiError | undefined>(undefined);
  const [notice, setNotice] = useState('');
  const all = list.data?.items ?? [];
  const rows = all.filter((queue) => showArchived || queue.status === 'active');
  const archivedCount = all.filter((queue) => queue.status === 'archived').length;
  const teamName = useReferenceNames(
    all.flatMap((queue) =>
      queue.defaultTeamId ? [{ kind: 'teams' as const, id: queue.defaultTeamId }] : [],
    ),
  );

  const changed = (message: string) => {
    setDialog(null);
    setNotice(message);
    list.reload();
    notifySidebarChanged();
  };

  const confirm = async (action: Action, queue: TicketQueue) => {
    setBusy(true);
    setConfirmError(undefined);
    try {
      if (action === 'archive') await ticketQueuesApi.archive(queue.id, queue.version);
      else if (action === 'restore') await ticketQueuesApi.restore(queue.id, queue.version);
      else await ticketQueuesApi.makeDefault(queue.id, queue.version);
      changed(t(`queues.done.${action}`, { name: queue.name }));
    } catch (cause) {
      setConfirmError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const columns: Column<TicketQueue>[] = [
    {
      key: 'prefix',
      header: t('queues.col.prefix'),
      sortValue: (queue) => queue.prefix,
      render: (queue) => <span className="incident-reference">{queue.prefix}</span>,
    },
    {
      key: 'name',
      header: t('queues.col.name'),
      sortValue: (queue) => queue.name,
      render: (queue) => (
        <span className="queue-name">
          <button
            type="button"
            className="link-button"
            onClick={() => setDialog({ kind: 'edit', queue })}
          >
            {queue.name}
          </button>
          <small>{queue.key}</small>
        </span>
      ),
    },
    {
      key: 'status',
      header: t('queues.col.status'),
      render: (queue) => (
        <span className="queue-badges">
          {queue.status === 'archived' ? (
            <Badge tone="neutral">{t('queues.status.archived')}</Badge>
          ) : (
            <Badge tone="success">{t('queues.status.active')}</Badge>
          )}
          {queue.isDefault ? <Badge tone="info">{t('queues.badge.intake')}</Badge> : null}
        </span>
      ),
    },
    {
      key: 'visibility',
      header: t('queues.col.visibility'),
      render: (queue) => t(`queues.visibility.${queue.visibility}`),
    },
    {
      key: 'routing',
      header: t('queues.col.routing'),
      render: (queue) => (queue.routingMode ? t(`queues.routing.${queue.routingMode}`) : '–'),
    },
    {
      key: 'team',
      header: t('queues.col.team'),
      render: (queue) =>
        queue.defaultTeamId
          ? (teamName('teams', queue.defaultTeamId) ?? t('query.referenceUnknown'))
          : '–',
    },
  ];

  const rowActions = (queue: TicketQueue): MenuItem[] => {
    const items: MenuItem[] = [
      {
        id: 'edit',
        label: t('queues.action.edit'),
        onSelect: () => setDialog({ kind: 'edit', queue }),
      },
      {
        id: 'grants',
        label: t('queues.action.grants'),
        onSelect: () => setDialog({ kind: 'grants', queue }),
      },
    ];
    if (queue.status === 'active') {
      items.push(
        queue.isDefault
          ? {
              id: 'default',
              label: t('queues.action.makeDefault'),
              disabledReason: t('queues.reason.alreadyDefault'),
              onSelect: () => undefined,
            }
          : queue.visibility !== 'public'
            ? {
                id: 'default',
                label: t('queues.action.makeDefault'),
                disabledReason: t('queues.reason.needsPublic'),
                onSelect: () => undefined,
              }
            : {
                id: 'default',
                label: t('queues.action.makeDefault'),
                onSelect: () => setDialog({ kind: 'confirm', action: 'makeDefault', queue }),
              },
        { id: 'sep', separator: true },
        queue.isDefault
          ? {
              id: 'archive',
              label: t('queues.action.archive'),
              danger: true,
              disabledReason: t('queues.reason.isDefault'),
              onSelect: () => undefined,
            }
          : {
              id: 'archive',
              label: t('queues.action.archive'),
              danger: true,
              onSelect: () => setDialog({ kind: 'confirm', action: 'archive', queue }),
            },
      );
    } else {
      items.push(
        { id: 'sep', separator: true },
        {
          id: 'restore',
          label: t('queues.action.restore'),
          onSelect: () => setDialog({ kind: 'confirm', action: 'restore', queue }),
        },
      );
    }
    return items;
  };

  return (
    <>
      <PageHeader
        eyebrow={t('queues.eyebrow')}
        title={t('queues.title')}
        intro={t('queues.intro')}
        actions={
          <Button variant="primary" onClick={() => setDialog({ kind: 'create' })}>
            <span aria-hidden="true">+</span> {t('queues.create.action')}
          </Button>
        }
      />
      {notice ? <Toast kind="success">{notice}</Toast> : null}
      {archivedCount > 0 ? (
        <Checkbox
          label={t('queues.showArchived', { count: archivedCount })}
          checked={showArchived}
          onChange={(event) => setShowArchived(event.target.checked)}
        />
      ) : null}
      <DataTable
        caption={t('queues.title')}
        columns={columns}
        rows={rows}
        rowKey={(queue) => queue.id}
        rowActions={rowActions}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('queues.empty')}
        totalCount={rows.length}
      />
      {dialog?.kind === 'create' ? (
        <QueueCreateDialog
          onClose={() => setDialog(null)}
          onCreated={(queue) =>
            changed(t('queues.done.create', { name: queue.name, prefix: queue.prefix }))
          }
        />
      ) : null}
      {dialog?.kind === 'edit' ? (
        <QueueEditDialog
          queue={dialog.queue}
          onClose={() => setDialog(null)}
          onSaved={(queue) => changed(t('queues.done.edit', { name: queue.name }))}
          onReload={() => {
            setDialog(null);
            list.reload();
          }}
        />
      ) : null}
      {dialog?.kind === 'grants' ? (
        <QueueGrantsDialog
          queueId={dialog.queue.id}
          onClose={() => setDialog(null)}
          onSaved={(queue) => changed(t('queues.done.grants', { name: queue.name }))}
        />
      ) : null}
      {dialog?.kind === 'confirm' ? (
        <ConfirmDialog
          title={t(`queues.confirm.${dialog.action}.title`, { name: dialog.queue.name })}
          message={
            <p>{t(`queues.confirm.${dialog.action}.body`, { prefix: dialog.queue.prefix })}</p>
          }
          confirmLabel={t(`queues.action.${dialog.action}`)}
          danger={dialog.action === 'archive'}
          busy={busy}
          error={confirmError ? t(errorMessageKey(confirmError)) : undefined}
          onCancel={() => {
            setDialog(null);
            setConfirmError(undefined);
          }}
          onConfirm={() => void confirm(dialog.action, dialog.queue)}
        />
      ) : null}
    </>
  );
}
