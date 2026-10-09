import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { FilterBar } from '../../platform/ui/FilterBar';
import { useState } from 'react';
import { useQueryList } from '../../platform/ui/query/useQueryList';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TaskTable } from './TaskTable';
import {
  taskPriorities,
  taskStatuses,
  type Task,
  type TaskPriority,
  type TaskStatus,
} from './types';

export function TasksScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [status, setStatus] = useState<TaskStatus | ''>('');
  const [priority, setPriority] = useState<TaskPriority | ''>(() => {
    const value = new URLSearchParams(window.location.search).get('priority');
    return taskPriorities.includes(value as TaskPriority) ? (value as TaskPriority) : '';
  });
  const [overdue, setOverdue] = useState(
    () => new URLSearchParams(window.location.search).get('overdue') === 'true',
  );
  const [mine, setMine] = useState(
    () => new URLSearchParams(window.location.search).get('mine') === 'true',
  );
  const [query, setQuery] = useState('');
  useFilterQuery({ priority, overdue, mine });
  const q = useDebouncedValue(query.trim(), 300);

  const workbench = useQueryList<Task>('tasks', { status, priority, overdue, mine, q });
  const { list } = workbench;

  const activeFilters = [
    ...(query
      ? [
          {
            key: 'search',
            label: `${t('tasks.filter.search')}: ${query}`,
            onRemove: () => {
              setQuery('');
            },
          },
        ]
      : []),
    ...(status
      ? [
          {
            key: 'status',
            label: t(`tasks.status.${status}`),
            onRemove: () => {
              setStatus('');
            },
          },
        ]
      : []),
    ...(priority
      ? [
          {
            key: 'priority',
            label: t(`tasks.priority.${priority}`),
            onRemove: () => {
              setPriority('');
            },
          },
        ]
      : []),
    ...(overdue
      ? [
          {
            key: 'overdue',
            label: t('tasks.filter.overdue'),
            onRemove: () => {
              setOverdue(false);
            },
          },
        ]
      : []),
    ...(mine
      ? [
          {
            key: 'mine',
            label: t('tasks.filter.mine'),
            onRemove: () => {
              setMine(false);
            },
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('nav.tasks')}
        intro={t('tasks.intro')}
        actions={
          <>
            <Link to="/tasks/boards" className="btn btn-secondary">
              {t('tasks.board.open')}
            </Link>
            {can('tasks.manage') ? (
              <Link to="/tasks/new" className="btn btn-primary">
                {t('tasks.create.action')}
              </Link>
            ) : null}
          </>
        }
      />
      <FilterBar
        activeFilters={activeFilters}
        role="search"
        onSubmit={(event) => event.preventDefault()}
      >
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
      </FilterBar>
      <TaskTable
        filterSummary={activeFilters.map((filter) => filter.label).join(' · ')}
        caption={t('nav.tasks')}
        emptyText={t('tasks.empty')}
        list={list}
        query={workbench}
      />
    </>
  );
}
