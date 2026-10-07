import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { availableActions, canEditTask, isOverdue, reasonRequired } from './actions';
import { tasksApi } from './api';
import { AssignDialog, EditDialog, ReasonDialog } from './TaskDialogs';
import { assigneeLabel, StatusBadge } from './TaskTable';
import type { Task, TaskAction } from './types';

type Dialog =
  { kind: 'reason'; action: 'block' | 'cancel' | 'reopen' } | { kind: 'edit' } | { kind: 'assign' };

function isReasonAction(action: TaskAction): action is 'block' | 'cancel' | 'reopen' {
  return reasonRequired.has(action);
}

export function TaskDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => tasksApi.get(id, signal), [id]);
  // A change returns the new state; it replaces the loaded task without another request.
  const [updated, setUpdated] = useState<Task | undefined>(undefined);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [actionError, setActionError] = useState<ApiError | undefined>(undefined);
  const [busyAction, setBusyAction] = useState<TaskAction | null>(null);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const task = updated ?? loaded.data;
  if (!task) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }

  const done = (next: Task) => {
    setUpdated(next);
    setDialog(null);
    setActionError(undefined);
  };

  const run = async (action: TaskAction) => {
    setBusyAction(action);
    setActionError(undefined);
    try {
      done(await tasksApi.transition(task.id, action, task.version));
    } catch (cause) {
      setActionError(asApiError(cause));
    } finally {
      setBusyAction(null);
    }
  };

  const actions = availableActions(task, can);
  const overdue = isOverdue(task, new Date());

  return (
    <>
      <PageHeader
        title={task.title}
        intro={undefined}
        actions={
          <>
            {canEditTask(task, can) ? (
              <>
                <Button onClick={() => setDialog({ kind: 'edit' })}>
                  {t('tasks.action.edit')}
                </Button>
                <Button onClick={() => setDialog({ kind: 'assign' })}>
                  {t('tasks.action.assign')}
                </Button>
              </>
            ) : null}
            {actions.map((action) => (
              <Button
                key={action}
                variant={action === 'complete' || action === 'start' ? 'primary' : 'secondary'}
                busy={busyAction === action}
                disabled={busyAction !== null}
                onClick={() =>
                  isReasonAction(action) ? setDialog({ kind: 'reason', action }) : void run(action)
                }
              >
                {t(`tasks.action.${action}`)}
              </Button>
            ))}
          </>
        }
      />
      <p>
        <Link to="/tasks">{t('tasks.back')}</Link>
      </p>
      {actionError ? <ApiErrorAlert error={actionError} onRetry={loaded.reload} /> : null}
      <dl className="facts">
        <dt>{t('tasks.col.status')}</dt>
        <dd>
          <StatusBadge status={task.status} />
        </dd>
        {task.statusReason ? (
          <>
            <dt>{t('tasks.fact.reason')}</dt>
            <dd>{task.statusReason}</dd>
          </>
        ) : null}
        <dt>{t('tasks.col.priority')}</dt>
        <dd>{t(`tasks.priority.${task.priority}`)}</dd>
        <dt>{t('tasks.col.assignee')}</dt>
        <dd>{assigneeLabel(task, t('tasks.assignee.none'))}</dd>
        <dt>{t('tasks.col.due')}</dt>
        <dd>
          {task.dueAt ? formatDateTime(locale, task.dueAt) : '–'}{' '}
          {overdue ? <Badge tone="danger">{t('tasks.overdue')}</Badge> : null}
        </dd>
        {task.completedAt ? (
          <>
            <dt>{t('tasks.fact.completedAt')}</dt>
            <dd>{formatDateTime(locale, task.completedAt)}</dd>
          </>
        ) : null}
        {task.contextType === 'deployment' && task.contextId ? (
          <>
            <dt>{t('tasks.fact.context')}</dt>
            <dd>
              <Link to={`/deployments/${encodeURIComponent(task.contextId)}`}>
                {t('tasks.context.deployment')}
              </Link>
            </dd>
          </>
        ) : null}
        <dt>{t('tasks.fact.created')}</dt>
        <dd>{formatDateTime(locale, task.createdAt)}</dd>
        <dt>{t('tasks.col.updated')}</dt>
        <dd>{formatDateTime(locale, task.updatedAt)}</dd>
      </dl>
      {task.description ? (
        <section>
          <h2>{t('tasks.field.description')}</h2>
          <p className="preline">{task.description}</p>
        </section>
      ) : null}
      {dialog?.kind === 'reason' ? (
        <ReasonDialog
          task={task}
          action={dialog.action}
          onClose={() => setDialog(null)}
          onDone={done}
        />
      ) : null}
      {dialog?.kind === 'edit' ? (
        <EditDialog task={task} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
      {dialog?.kind === 'assign' ? (
        <AssignDialog task={task} onClose={() => setDialog(null)} onDone={done} />
      ) : null}
    </>
  );
}
