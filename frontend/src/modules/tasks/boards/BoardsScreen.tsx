import { useState } from 'react';
import type { ApiError } from '../../../platform/api/client';
import { asApiError, useAsync } from '../../../platform/api/useAsync';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../../platform/i18n/i18n';
import { Link, navigate } from '../../../platform/router/Router';
import { useSession } from '../../../platform/session/SessionProvider';
import { Badge } from '../../../platform/ui/Alert';
import { ApiErrorAlert } from '../../../platform/ui/ApiErrorAlert';
import { Button } from '../../../platform/ui/Button';
import { PageHeader } from '../../../platform/ui/PageHeader';
import { notifySidebarChanged } from '../../../platform/ui/views/api';
import { EmptyState, Skeleton } from '../../../platform/ui/Workspace';
import { boardsApi } from './api';
import { BoardCreateDialog } from './BoardCreateDialog';
import { boardSection, type BoardSection } from './model';
import type { TaskBoard } from './types';

const sections: readonly BoardSection[] = ['mine', 'team', 'shared'];

/** Board switcher: the Boards the caller owns, that belong to a Team, or that were shared. */
export function BoardsScreen() {
  const { t } = useI18n();
  const { session } = useSession();
  const me = session?.userId ?? '';
  const list = useAsync((signal) => boardsApi.list(undefined, signal), []);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [busy, setBusy] = useState<string>();

  const items = list.data?.items ?? [];
  const active = items.filter((board) => !board.archived);
  const archived = items.filter((board) => board.archived && board.access === 'owner');

  const restore = async (board: TaskBoard) => {
    setBusy(board.id);
    setError(undefined);
    try {
      await boardsApi.restore(board.id, board.version);
      notifySidebarChanged();
      list.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(undefined);
    }
  };

  const grid = (boards: TaskBoard[]) => (
    <ul className="board-list">
      {boards.map((board) => (
        <li key={board.id} className="board-list-item">
          <Link to={`/tasks/boards/${encodeURIComponent(board.id)}`} className="board-list-link">
            <strong>{board.name}</strong>
            {board.description ? <span>{board.description}</span> : null}
            <small>
              {[
                board.ownerTeamName ?? board.ownerName,
                t('tasks.board.list.columns', { count: board.columns.length }),
                board.access === 'use' ? t('views.readOnly') : undefined,
              ]
                .filter(Boolean)
                .join(' · ')}
            </small>
          </Link>
        </li>
      ))}
    </ul>
  );

  return (
    <>
      <PageHeader
        title={t('tasks.board.listTitle')}
        intro={t('tasks.board.listIntro')}
        actions={
          <>
            <Link to="/tasks" className="btn btn-secondary">
              {t('tasks.board.backToTasks')}
            </Link>
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('tasks.board.create.action')}
            </Button>
          </>
        }
      />
      {error ? <ApiErrorAlert error={error} /> : null}
      {list.error ? <ApiErrorAlert error={list.error} onRetry={list.reload} /> : null}
      {list.loading && !list.data ? <Skeleton lines={4} /> : null}
      {list.data && active.length === 0 ? (
        <EmptyState
          title={t('tasks.board.listEmpty')}
          description={t('tasks.board.listEmptyHint')}
          action={
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('tasks.board.create.action')}
            </Button>
          }
        />
      ) : null}
      {sections.map((key) => {
        const boards = active.filter((board) => boardSection(board, me) === key);
        if (boards.length === 0) return null;
        return (
          <section key={key} aria-label={t(`tasks.board.section.${key}` as MessageKey)}>
            <h2>{t(`tasks.board.section.${key}` as MessageKey)}</h2>
            {grid(boards)}
          </section>
        );
      })}
      {archived.length > 0 ? (
        <section aria-label={t('tasks.board.section.archived')}>
          <h2>{t('tasks.board.section.archived')}</h2>
          <ul className="board-list">
            {archived.map((board) => (
              <li key={board.id} className="board-list-item board-list-archived">
                <strong>{board.name}</strong>
                <Badge tone="neutral">{t('tasks.board.archivedBadge')}</Badge>
                <Button busy={busy === board.id} onClick={() => void restore(board)}>
                  {t('tasks.board.action.restore')}
                </Button>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      {creating ? (
        <BoardCreateDialog
          onClose={() => setCreating(false)}
          onCreated={(board) => {
            setCreating(false);
            navigate(`/tasks/boards/${encodeURIComponent(board.id)}`);
          }}
        />
      ) : null}
    </>
  );
}
