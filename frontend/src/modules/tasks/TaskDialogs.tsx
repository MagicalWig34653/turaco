import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select, TextArea } from '../../platform/ui/Field';
import { tasksApi } from './api';
import { AssigneePicker, type Assignee, type AssigneeType } from './AssigneePicker';
import { TaskForm, type TaskFormValues } from './TaskForm';
import type { Task, TaskAction } from './types';

type Done = (task: Task) => void;

/** Runs a task call, tracking busy and error state for a dialog. */
function useSubmit(onDone: Done) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const run = async (call: () => Promise<Task>) => {
    setBusy(true);
    setError(undefined);
    try {
      onDone(await call());
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return { busy, error, run };
}

export function ReasonDialog({
  task,
  action,
  onClose,
  onDone,
}: {
  task: Task;
  action: Extract<TaskAction, 'block' | 'cancel' | 'reopen'>;
  onClose: () => void;
  onDone: Done;
}) {
  const { t } = useI18n();
  const [reason, setReason] = useState('');
  const { busy, error, run } = useSubmit(onDone);
  const submit = (event: FormEvent) => {
    event.preventDefault();
    void run(() => tasksApi.transition(task.id, action, task.version, reason.trim()));
  };
  return (
    <Dialog title={t(`tasks.reason.${action}.title`)} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextArea
          label={t('tasks.reason.label')}
          hint={t('tasks.reason.hint')}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          maxLength={500}
          rows={3}
          required
          autoFocus
        />
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={action === 'cancel' ? 'danger' : 'primary'}
            busy={busy}
            disabled={reason.trim() === ''}
          >
            {t(`tasks.action.${action}`)}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

export function EditDialog({
  task,
  onClose,
  onDone,
}: {
  task: Task;
  onClose: () => void;
  onDone: Done;
}) {
  const { t } = useI18n();
  const { busy, error, run } = useSubmit(onDone);
  const save = (values: TaskFormValues) =>
    void run(() =>
      tasksApi.update(task.id, {
        expectedVersion: task.version,
        title: values.title,
        description: values.description,
        priority: values.priority,
        dueAt: values.dueAt,
      }),
    );
  return (
    <Dialog title={t('tasks.edit.title')} onClose={onClose} wide>
      {error ? <ApiErrorAlert error={error} /> : null}
      <TaskForm
        initial={task}
        submitLabel={t('action.save')}
        busy={busy}
        onSubmit={save}
        onCancel={onClose}
      />
    </Dialog>
  );
}

export function AssignDialog({
  task,
  onClose,
  onDone,
}: {
  task: Task;
  onClose: () => void;
  onDone: Done;
}) {
  const { t } = useI18n();
  const [type, setType] = useState<AssigneeType>('user');
  const [assignee, setAssignee] = useState<Assignee | null>(null);
  const { busy, error, run } = useSubmit(onDone);
  const hasAssignment = task.assignedUserId !== null || task.assignedTeamId !== null;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!assignee) return;
    void run(() =>
      tasksApi.assign(task.id, {
        expectedVersion: task.version,
        ...(type === 'user' ? { userId: assignee.id } : { teamId: assignee.id }),
      }),
    );
  };

  return (
    <Dialog title={t('tasks.assign.title')} onClose={onClose} wide>
      <form className="form" onSubmit={submit}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <p className="field-hint">{t('tasks.assign.hint')}</p>
        <Select
          label={t('tasks.assign.type')}
          value={type}
          onChange={(event) => {
            setType(event.target.value as AssigneeType);
            setAssignee(null);
          }}
          options={[
            { value: 'user', label: t('tasks.assign.user') },
            { value: 'team', label: t('tasks.assign.team') },
          ]}
        />
        <AssigneePicker key={type} type={type} value={assignee} onChange={setAssignee} />
        <div className="dialog-actions">
          {hasAssignment ? (
            <Button
              variant="danger"
              busy={busy}
              onClick={() => void run(() => tasksApi.unassign(task.id, task.version))}
            >
              {t('tasks.action.unassign')}
            </Button>
          ) : null}
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!assignee}>
            {t('tasks.action.assign')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
