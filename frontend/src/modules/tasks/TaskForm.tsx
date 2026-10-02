import { useState } from 'react';
import type { FormEvent } from 'react';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { isoToLocalInput, localInputToIso } from '../../platform/format/format';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { taskPriorities, type Task, type TaskPriority } from './types';

export type TaskFormValues = {
  title: string;
  description: string;
  priority: TaskPriority;
  /** RFC 3339, or null when empty. */
  dueAt: string | null;
};

type Props = {
  initial?: Pick<Task, 'title' | 'description' | 'priority' | 'dueAt'>;
  submitLabel: string;
  busy: boolean;
  onSubmit: (values: TaskFormValues) => void;
  onCancel: () => void;
};

/** Title, description, priority and due date; shared by task creation and editing. */
export function TaskForm({ initial, submitLabel, busy, onSubmit, onCancel }: Props) {
  const { t } = useI18n();
  const [title, setTitle] = useState(initial?.title ?? '');
  const [description, setDescription] = useState(initial?.description ?? '');
  const [priority, setPriority] = useState<TaskPriority>(initial?.priority ?? 'normal');
  const [due, setDue] = useState(isoToLocalInput(initial?.dueAt));

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (title.trim() === '') return;
    onSubmit({ title: title.trim(), description, priority, dueAt: localInputToIso(due) ?? null });
  };

  return (
    <form className="form" onSubmit={submit}>
      <TextField
        label={t('tasks.field.title')}
        value={title}
        onChange={(event) => setTitle(event.target.value)}
        maxLength={200}
        required
        autoFocus
      />
      <TextArea
        label={t('tasks.field.description')}
        value={description}
        onChange={(event) => setDescription(event.target.value)}
        maxLength={10000}
        rows={6}
      />
      <Select
        label={t('tasks.field.priority')}
        value={priority}
        onChange={(event) => setPriority(event.target.value as TaskPriority)}
        options={taskPriorities.map((value) => ({ value, label: t(`tasks.priority.${value}`) }))}
      />
      <TextField
        label={t('tasks.field.due')}
        hint={t('tasks.field.dueHint')}
        type="datetime-local"
        value={due}
        onChange={(event) => setDue(event.target.value)}
      />
      <div className="form-actions">
        <Button onClick={onCancel}>{t('action.cancel')}</Button>
        <Button type="submit" variant="primary" busy={busy} disabled={title.trim() === ''}>
          {submitLabel}
        </Button>
      </div>
    </form>
  );
}
