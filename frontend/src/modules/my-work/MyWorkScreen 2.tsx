import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { PageHeader } from '../../platform/ui/PageHeader';
import { tasksApi } from '../tasks/api';
import { TaskTable } from '../tasks/TaskTable';

/** The caller's unfinished tasks (assigned to them or to one of their Teams). */
export function MyWorkScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const list = usePagedList((cursor, signal) => tasksApi.myWork(cursor, signal), []);
  return (
    <>
      <PageHeader
        title={t('nav.myWork')}
        intro={t('myWork.intro')}
        actions={
          can('tasks.manage') ? (
            <Link to="/tasks/new" className="btn btn-primary">
              {t('tasks.create.action')}
            </Link>
          ) : null
        }
      />
      <TaskTable caption={t('nav.myWork')} emptyText={t('myWork.empty')} list={list} />
    </>
  );
}
