import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { PageHeader } from '../../platform/ui/PageHeader';
import { tasksApi } from './api';
import { TaskTable } from './TaskTable';
import {
  taskPriorities,
  taskStatuses,
  type TaskFilter,
  type TaskPriority,
  type TaskStatus,
} from './types';

export function TasksScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState<TaskStatus | ''>('');
  const [priority, setPriority] = useState<TaskPriority | ''>('');
  const [overdue, setOverdue] = useState(
    () => new URLSearchParams(window.location.search).get('overdue') === 'true',
  );
  const [mine, setMine] = useState(false);
  const [query, setQuery] = useState('');
  const q = useDebouncedValue(query.trim(), 300);

  const filter: TaskFilter = {
    ...(status ? { status } : {}),
    ...(priority ? { priority } : {}),
    ...(overdue ? { overdue: true } : {}),
    ...(mine ? { mine: true } : {}),
    ...(q ? { q } : {}),
  };
  const list = usePagedList(
    (cursor, signal) => tasksApi.list(filter, cursor, signal),
    [status, priority, overdue, mine, q],
  );

  return (
    <>
      <PageHeader
        title={t('nav.tasks')}
        intro={t('tasks.intro')}
        actions={
          can('tasks.manage') ? (
            <Link to="/tasks/new" className="btn btn-primary">
              {t('tasks.create.action')}
            </Link>
          ) : null
        }
      />
      <form className="filters" role="search" onSubmit={(event) => event.preventDefault()}>
        <TextField
          label={t('tasks.filter.search')}
          type="search"
          value={query}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuery(event.target.value)}
        />
        <Select
          label={t('tasks.col.status')}
          value={status}
          onChange={(event) => setStatus(event.target.value as TaskStatus | '')}
          options={[
            { value: '', label: t('tasks.filter.anyStatus') },
            ...taskStatuses.map((value) => ({ value, label: t(`tasks.status.${value}`) })),
          ]}
        />
        <Select
          label={t('tasks.col.priority')}
          value={priority}
          onChange={(event) => setPriority(event.target.value as TaskPriority | '')}
          options={[
            { value: '', label: t('tasks.filter.anyPriority') },
            ...taskPriorities.map((value) => ({ value, label: t(`tasks.priority.${value}`) })),
          ]}
        />
        <Checkbox
          label={t('tasks.filter.overdue')}
          checked={overdue}
          onChange={(event) => setOverdue(event.target.checked)}
        />
        <Checkbox
          label={t('tasks.filter.mine')}
          checked={mine}
          onChange={(event) => setMine(event.target.checked)}
        />
      </form>
      <TaskTable caption={t('nav.tasks')} emptyText={t('tasks.empty')} list={list} />
    </>
  );
}
