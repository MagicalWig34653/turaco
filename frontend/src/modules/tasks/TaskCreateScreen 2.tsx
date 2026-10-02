import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { navigate } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { PageHeader } from '../../platform/ui/PageHeader';
import { tasksApi } from './api';
import { TaskForm, type TaskFormValues } from './TaskForm';

export function TaskCreateScreen() {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const create = async (values: TaskFormValues) => {
    setBusy(true);
    setError(undefined);
    try {
      const task = await tasksApi.create({
        title: values.title,
        description: values.description,
        priority: values.priority,
        dueAt: values.dueAt,
      });
      navigate(`/tasks/${encodeURIComponent(task.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={t('tasks.create.title')} intro={t('tasks.create.intro')} />
      {error ? <ApiErrorAlert error={error} /> : null}
      <TaskForm
        submitLabel={t('tasks.create.action')}
        busy={busy}
        onSubmit={(values) => void create(values)}
        onCancel={() => navigate('/tasks')}
      />
    </>
  );
}
