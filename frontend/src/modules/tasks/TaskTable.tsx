import { TableDate } from '../../platform/ui/TableDate';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import type { PagedState } from '../../platform/api/useAsync';
import { isOverdue } from './actions';
import type { Task, TaskStatus } from './types';

const statusTone: Record<TaskStatus, 'neutral' | 'success' | 'warning' | 'info'> = {
  open: 'neutral',
  in_progress: 'info',
  blocked: 'warning',
  completed: 'success',
  cancelled: 'neutral',
};

export function StatusBadge({ status }: { status: TaskStatus }) {
  const { t } = useI18n();
  return (
    <Badge tone={statusTone[status]} live={status === 'in_progress'}>
      {t(`tasks.status.${status}`)}
    </Badge>
  );
}

export function assigneeLabel(task: Task, none: string): string {
  const parts = [
    task.assignedUserName ?? task.assignedUserId,
    task.assignedTeamName ?? task.assignedTeamId,
  ];
  const text = parts.filter((part): part is string => Boolean(part)).join(' · ');
  return text || none;
}

/** Task list shared by the task and My Work screens. */
export function TaskTable({
  caption,
  filterSummary = '',
  emptyText,
  list,
}: {
  caption: string;
  filterSummary?: string;
  emptyText: string;
  list: PagedState<Task>;
}) {
  const { t } = useI18n();
  const now = new Date();
  const columns: Column<Task>[] = [
    {
      key: 'title',
      sortValue: (task) => task.title,
      header: t('tasks.col.title'),
      render: (task) => <Link to={`/tasks/${encodeURIComponent(task.id)}`}>{task.title}</Link>,
    },
    {
      key: 'status',
      sortValue: (task) => task.status,
      header: t('tasks.col.status'),
      render: (task) => <StatusBadge status={task.status} />,
    },
    {
      key: 'priority',
      sortValue: (task) => ({ low: 0, normal: 1, high: 2, urgent: 3 })[task.priority],
      header: t('tasks.col.priority'),
      render: (task) => t(`tasks.priority.${task.priority}`),
    },
    {
      key: 'assignee',
      header: t('tasks.col.assignee'),
      render: (task) => assigneeLabel(task, t('tasks.assignee.none')),
    },
    {
      key: 'due',
      sortValue: (task) => task.dueAt,
      header: t('tasks.col.due'),
      render: (task) => (
        <>
          <TableDate value={task.dueAt} />{' '}
          {isOverdue(task, now) ? <Badge tone="danger">{t('tasks.overdue')}</Badge> : null}
        </>
      ),
    },
  ];
  return (
    <DataTable
      filterSummary={filterSummary}
      caption={caption}
      columns={columns}
      rows={list.items}
      rowKey={(task) => task.id}
      loading={list.loading}
      error={list.error}
      onRetry={list.reload}
      emptyText={emptyText}
      hasMore={list.hasMore}
      loadingMore={list.loadingMore}
      loadMoreError={list.loadMoreError}
      onLoadMore={list.loadMore}
    />
  );
}
