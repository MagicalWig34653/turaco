import { useEffect, useRef, useState } from 'react';
import type { DragEvent } from 'react';
import { type ApiError } from '../../../platform/api/client';
import { errorMessageKey } from '../../../platform/api/errorMessages';
import { asApiError, useAsync } from '../../../platform/api/useAsync';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../../platform/router/Router';
import { useSession } from '../../../platform/session/SessionProvider';
import { Alert } from '../../../platform/ui/Alert';
import { ApiErrorAlert } from '../../../platform/ui/ApiErrorAlert';
import { Button } from '../../../platform/ui/Button';
import { copyContextText, useContextMenu, type MenuItem } from '../../../platform/ui/ContextMenu';
import { ConfirmDialog } from '../../../platform/ui/Dialog';
import { Checkbox, Select, TextField } from '../../../platform/ui/Field';
import { FilterBar } from '../../../platform/ui/FilterBar';
import { ReasonDialog } from '../../../platform/ui/ReasonDialog';
import { PageHeader } from '../../../platform/ui/PageHeader';
import { listConditions } from '../../../platform/ui/query/filterModel';
import { ShareDialog } from '../../../platform/ui/views/ShareDialog';
import { notifySidebarChanged, viewsApi, type SavedView } from '../../../platform/ui/views/api';
import { abilities, addPin, pinPayload, removePin } from '../../../platform/ui/views/model';
import { Skeleton } from '../../../platform/ui/Workspace';
import { availableActions, isTerminal, reasonRequired } from '../actions';
import { tasksApi } from '../api';
import { taskPriorities, type TaskAction, type TaskPriority } from '../types';
import { boardsApi } from './api';
import { BoardFilterDialog, BoardSettingsDialog } from './BoardDialogs';
import { BoardGrid, focusNeighbour, type DragState } from './BoardGrid';
import { CardDrawer } from './CardDrawer';
import {
  emptyQuickFilter,
  findCard,
  keyboardTarget,
  laneKeyOf,
  moveTargets,
  omitKey,
  planAction,
  planMove,
  quickFilterActive,
  sortedColumns,
  type KeyboardDirection,
  type MoveDecision,
  type MovePlan,
} from './model';
import type { BoardCard } from './types';
import { useBoard } from './useBoard';

type DialogState =
  | { kind: 'settings' }
  | { kind: 'filter' }
  | { kind: 'share'; view: SavedView }
  | { kind: 'archive' }
  | { kind: 'reason'; plan: MovePlan; action: 'block' | 'cancel' | 'reopen' }
  | null;

const reasonActions = new Set<TaskAction>(['block', 'cancel', 'reopen']);

export function BoardScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const me = session?.userId;
  const menu = useContextMenu();
  const [drag, setDrag] = useState<DragState | null>(null);
  const [dialog, setDialog] = useState<DialogState>(null);
  const [drawerId, setDrawerId] = useState<string>();
  const [quick, setQuick] = useState(emptyQuickFilter);
  const [laneOverride, setLaneOverride] = useState<boolean>();
  const [collapseOverride, setCollapseOverride] = useState<Record<string, boolean>>({});
  const [actionError, setActionError] = useState<ApiError>();
  const [announcement, setAnnouncement] = useState('');
  const [busyCard, setBusyCard] = useState<string>();
  const [now] = useState(() => new Date());
  const gridRef = useRef<HTMLDivElement | null>(null);
  const focusAfter = useRef<string | undefined>(undefined);

  const state = useBoard(id, drag !== null || dialog !== null);
  const { board, cards } = state;
  const boards = useAsync((signal) => boardsApi.list(undefined, signal), []);
  const viewRead = useAsync(
    (signal) => (board ? viewsApi.get(board.viewId, signal) : Promise.resolve(undefined)),
    [board?.viewId, board?.viewVersion],
  );
  const view = viewRead.data;
  const [pinOverride, setPinOverride] = useState<boolean>();

  // After a keyboard move the card remounts in its new column; give it focus again.
  useEffect(() => {
    const taskId = focusAfter.current;
    if (!taskId) return;
    const element = gridRef.current?.querySelector<HTMLElement>(
      `[data-card-id="${CSS.escape(taskId)}"] .board-card-main`,
    );
    if (element) {
      element.focus();
      focusAfter.current = undefined;
    }
  }, [cards]);

  const drawerCard = drawerId && cards ? findCard(cards, drawerId)?.card : undefined;
  useEffect(() => {
    if (drawerId && cards && !drawerCard) setDrawerId(undefined);
  }, [drawerId, cards, drawerCard]);

  if (state.error) {
    return (
      <>
        <PageHeader title={t('tasks.board.title')} />
        <ApiErrorAlert error={state.error} onRetry={state.reload} />
        <p>
          <Link to="/tasks/boards">{t('tasks.board.backToList')}</Link>
        </p>
      </>
    );
  }
  if (!board || !cards) {
    return (
      <>
        <PageHeader title={t('tasks.board.title')} />
        <Skeleton lines={6} />
      </>
    );
  }

  const columns = sortedColumns(board);
  const lanes = laneOverride ?? board.swimlane === 'assignee';
  const collapsed = Object.fromEntries(
    columns.map((column) => [column.id, collapseOverride[column.id] ?? column.collapsed]),
  );
  const rights = view ? abilities(view, can) : undefined;
  const pinned = pinOverride ?? view?.pinned ?? false;
  const editable = board.canEdit && !board.archived;
  const conditions = listConditions(board.filter?.root).length;
  const unavailableWarning = Object.values(cards).some((column) =>
    column.warnings?.some((warning) => warning.code === 'query.field_unavailable'),
  );
  const totalCards = Object.values(cards).reduce((sum, column) => sum + column.items.length, 0);

  const announce = (text: string) => setAnnouncement(text);
  const report = (cause: unknown) => {
    const failure = asApiError(cause);
    setActionError(failure);
    announce(t(errorMessageKey(failure)));
  };
  const columnTitle = (columnId: string | null) =>
    columns.find((column) => column.id === columnId)?.title ?? '';

  const execute = async (plan: MovePlan, reason?: string) => {
    const title = findCard(cards, plan.taskId)?.card.title ?? '';
    await state.moveCard(plan, reason);
    announce(
      plan.toColumnId === null
        ? t('tasks.board.announce.left', { title })
        : t('tasks.board.announce.moved', { title, column: columnTitle(plan.toColumnId) }),
    );
  };
  const begin = (decision: MoveDecision) => {
    if (!decision.ok) {
      if (decision.refusal !== 'noop' && decision.refusal !== 'unknown')
        announce(t(`tasks.board.refusal.${decision.refusal}`));
      return;
    }
    setActionError(undefined);
    const { plan } = decision;
    if (plan.needsReason && plan.action && reasonActions.has(plan.action)) {
      setDialog({
        kind: 'reason',
        plan,
        action: plan.action as 'block' | 'cancel' | 'reopen',
      });
      return;
    }
    void execute(plan).catch(report);
  };

  const evaluateDrop = (taskId: string, columnId: string, index: number) =>
    planMove({ board, cards, taskId, toColumnId: columnId, index, can });
  const runAction = (card: BoardCard, action: TaskAction) =>
    begin(planAction({ board, cards, taskId: card.id, action, can }));

  const assignToMe = async (card: BoardCard) => {
    if (!me) return;
    setBusyCard(card.id);
    setActionError(undefined);
    try {
      state.patchTask(
        await tasksApi.assign(card.id, { expectedVersion: card.version, userId: me }),
      );
      announce(t('tasks.board.announce.assigned', { title: card.title }));
    } catch (cause) {
      report(cause);
      state.refresh();
    } finally {
      setBusyCard(undefined);
    }
  };

  const keyboardMove = (card: BoardCard, direction: KeyboardDirection) => {
    const target = keyboardTarget(board, cards, card.id, direction);
    if (!target) {
      announce(t('tasks.board.announce.edge'));
      return;
    }
    focusAfter.current = card.id;
    begin(
      planMove({
        board,
        cards,
        taskId: card.id,
        toColumnId: target.columnId,
        index: target.index,
        can,
      }),
    );
  };

  const cardMenu = (card: BoardCard): MenuItem[] => {
    const items: MenuItem[] = [
      { id: 'open', label: t('tasks.board.card.open'), onSelect: () => setDrawerId(card.id) },
    ];
    if (can('tasks.manage') && !isTerminal(card.status) && card.assignedUserId !== me)
      items.push({
        id: 'assign',
        label: t('tasks.board.card.assignMe'),
        onSelect: () => void assignToMe(card),
      });
    for (const action of availableActions(card, can)) {
      const label = t(`tasks.action.${action}`);
      items.push({
        id: `action-${action}`,
        label: reasonRequired.has(action) ? `${label}…` : label,
        onSelect: () => runAction(card, action),
      });
    }
    const targets = moveTargets({ board, cards, taskId: card.id, can });
    items.push({ id: 'sep-move', separator: true });
    for (const target of targets) {
      const label = t('tasks.board.card.moveTo', { column: target.column.title });
      items.push(
        target.refusal && target.refusal !== 'unknown'
          ? {
              id: `move-${target.column.id}`,
              label,
              disabledReason: t(`tasks.board.refusal.${target.refusal}`),
              onSelect: () => undefined,
            }
          : {
              id: `move-${target.column.id}`,
              label,
              onSelect: () =>
                begin(
                  planMove({ board, cards, taskId: card.id, toColumnId: target.column.id, can }),
                ),
            },
      );
    }
    const here = findCard(cards, card.id);
    if (here) {
      const top = planMove({
        board,
        cards,
        taskId: card.id,
        toColumnId: here.columnId,
        index: 0,
        can,
      });
      items.push(
        top.ok
          ? {
              id: 'move-top',
              label: t('tasks.board.card.moveTop'),
              onSelect: () => begin(top),
            }
          : {
              id: 'move-top',
              label: t('tasks.board.card.moveTop'),
              disabledReason:
                top.refusal === 'noop'
                  ? t('tasks.board.refusal.noop')
                  : t(
                      `tasks.board.refusal.${top.refusal === 'unknown' ? 'needs_edit' : top.refusal}`,
                    ),
              onSelect: () => undefined,
            },
      );
    }
    items.push(
      { id: 'sep-link', separator: true },
      {
        id: 'copy',
        label: t('tasks.board.card.copyLink'),
        onSelect: () =>
          void copyContextText(
            `${window.location.origin}/tasks/${encodeURIComponent(card.id)}`,
          ).then((ok) =>
            announce(ok ? t('tasks.board.card.linkCopied') : t('tasks.board.card.linkFailed')),
          ),
      },
      {
        id: 'page',
        label: t('tasks.board.card.openPage'),
        onSelect: () => navigate(`/tasks/${encodeURIComponent(card.id)}`),
      },
    );
    return items;
  };

  const callbacks = {
    onOpen: (card: BoardCard) => setDrawerId(card.id),
    onMenu: (card: BoardCard, opener: HTMLElement, point?: { x: number; y: number }) => {
      const items = cardMenu(card);
      if (point) menu.openAtPoint(items, point, opener, card.title);
      else menu.openAtElement(items, opener, card.title);
    },
    onDragStart: (card: BoardCard, event: DragEvent<HTMLElement>) => {
      event.dataTransfer.setData('text/plain', card.id);
      event.dataTransfer.effectAllowed = 'move';
      setDrag({ taskId: card.id, laneKey: laneKeyOf(card) });
    },
    onDragEnd: () => setDrag(null),
    onKeyboardMove: keyboardMove,
    onFocusMove: (card: BoardCard, direction: KeyboardDirection) => {
      const root = gridRef.current;
      const from = root?.querySelector<HTMLElement>(
        `[data-card-id="${CSS.escape(card.id)}"] .board-card-main`,
      );
      if (root && from) focusNeighbour(root, from, direction);
    },
  };

  const toggleCollapse = (column: (typeof columns)[number]) => {
    const next = !collapsed[column.id];
    setCollapseOverride((previous) => ({ ...previous, [column.id]: next }));
    if (!editable) return;
    boardsApi
      .columns(board.id, board.version, {
        operation: 'collapse',
        columnId: column.id,
        collapsed: next,
      })
      .then(
        (updated) => {
          state.setBoard(updated);
          setCollapseOverride((previous) => omitKey(previous, column.id));
        },
        (cause: unknown) => {
          setCollapseOverride((previous) => omitKey(previous, column.id));
          report(cause);
          state.refresh();
        },
      );
  };

  const toggleLanes = (next: boolean) => {
    setLaneOverride(next);
    if (!editable) return;
    boardsApi
      .update(board.id, { expectedVersion: board.version, swimlane: next ? 'assignee' : 'none' })
      .then(
        (updated) => {
          state.setBoard(updated);
          setLaneOverride(undefined);
        },
        (cause: unknown) => {
          setLaneOverride(undefined);
          report(cause);
          state.refresh();
        },
      );
  };

  const togglePin = async () => {
    if (!view) return;
    const next = !pinned;
    try {
      const current = await viewsApi.pins();
      const items = next ? addPin(current.items, view) : removePin(current.items, view.id);
      await viewsApi.replacePins(pinPayload(items));
      setPinOverride(next);
      notifySidebarChanged();
      announce(next ? t('views.pinnedDone') : t('views.unpinnedDone'));
    } catch (cause) {
      report(cause);
    }
  };

  const archive = async () => {
    await boardsApi.archive(board.id, board.version);
    notifySidebarChanged();
    navigate('/tasks/boards');
  };

  const activeFilters = [
    ...(quick.text.trim()
      ? [
          {
            key: 'text',
            label: `${t('tasks.board.quick.search')}: ${quick.text.trim()}`,
            onRemove: () => setQuick({ ...quick, text: '' }),
          },
        ]
      : []),
    ...(quick.priority
      ? [
          {
            key: 'priority',
            label: t(`tasks.priority.${quick.priority}`),
            onRemove: () => setQuick({ ...quick, priority: '' }),
          },
        ]
      : []),
    ...(quick.mine
      ? [
          {
            key: 'mine',
            label: t('tasks.board.quick.mine'),
            onRemove: () => setQuick({ ...quick, mine: false }),
          },
        ]
      : []),
    ...(quick.overdue
      ? [
          {
            key: 'overdue',
            label: t('tasks.filter.overdue'),
            onRemove: () => setQuick({ ...quick, overdue: false }),
          },
        ]
      : []),
  ];

  const boardOptions = [...(boards.data?.items ?? []).filter((entry) => !entry.archived)];
  if (!boardOptions.some((entry) => entry.id === board.id)) boardOptions.unshift(board);

  return (
    <>
      <PageHeader
        title={board.name}
        eyebrow={
          board.ownerTeamName
            ? t('tasks.board.teamBoard', { team: board.ownerTeamName })
            : undefined
        }
        intro={board.description || undefined}
        actions={
          <>
            <Link to="/tasks/boards" className="btn btn-secondary">
              {t('tasks.board.allBoards')}
            </Link>
            <Button onClick={() => setDialog({ kind: 'filter' })}>
              {conditions > 0
                ? t('tasks.board.action.filterCount', { count: conditions })
                : t('tasks.board.action.filter')}
            </Button>
            {editable ? (
              <Button onClick={() => setDialog({ kind: 'settings' })}>
                {t('tasks.board.action.settings')}
              </Button>
            ) : null}
            {rights?.canShare && view && !board.archived ? (
              <Button onClick={() => setDialog({ kind: 'share', view })}>
                {t('tasks.board.action.share')}
              </Button>
            ) : null}
            {rights?.canPin && !board.archived ? (
              <Button aria-pressed={pinned} onClick={() => void togglePin()}>
                {pinned ? t('views.unpin') : t('views.pin')}
              </Button>
            ) : null}
            {board.access === 'owner' && !board.archived ? (
              <Button variant="danger" onClick={() => setDialog({ kind: 'archive' })}>
                {t('tasks.board.action.archive')}
              </Button>
            ) : null}
          </>
        }
      />

      {board.archived ? <Alert kind="warning">{t('tasks.board.archivedNotice')}</Alert> : null}
      {state.refreshFailed ? (
        <Alert kind="warning">
          <p>{t('tasks.board.refreshFailed')}</p>
          <Button onClick={state.refresh}>{t('action.refresh')}</Button>
        </Alert>
      ) : null}
      {unavailableWarning ? <Alert kind="info">{t('tasks.board.filterWarning')}</Alert> : null}
      {actionError ? (
        <div className="board-error">
          <ApiErrorAlert error={actionError} />
          <Button onClick={() => setActionError(undefined)}>{t('tasks.board.dismiss')}</Button>
        </div>
      ) : null}
      {state.loadMoreError ? <ApiErrorAlert error={state.loadMoreError} /> : null}

      <FilterBar
        role="search"
        aria-label={t('tasks.board.quick.title')}
        activeFilters={activeFilters}
        onClear={() => setQuick(emptyQuickFilter)}
        primaryCount={6}
      >
        <Select
          label={t('tasks.board.switcher')}
          value={board.id}
          onChange={(event) => navigate(`/tasks/boards/${encodeURIComponent(event.target.value)}`)}
          options={boardOptions.map((entry) => ({ value: entry.id, label: entry.name }))}
        />
        <TextField
          label={t('tasks.board.quick.search')}
          type="search"
          value={quick.text}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setQuick({ ...quick, text: event.target.value })}
        />
        <Select
          label={t('tasks.col.priority')}
          value={quick.priority}
          onChange={(event) =>
            setQuick({ ...quick, priority: event.target.value as TaskPriority | '' })
          }
          options={[
            { value: '', label: t('tasks.filter.anyPriority') },
            ...taskPriorities.map((value) => ({ value, label: t(`tasks.priority.${value}`) })),
          ]}
        />
        <Checkbox
          label={t('tasks.board.quick.mine')}
          checked={quick.mine}
          onChange={(event) => setQuick({ ...quick, mine: event.target.checked })}
        />
        <Checkbox
          label={t('tasks.filter.overdue')}
          checked={quick.overdue}
          onChange={(event) => setQuick({ ...quick, overdue: event.target.checked })}
        />
        <Checkbox
          label={t('tasks.board.swimlane')}
          checked={lanes}
          onChange={(event) => toggleLanes(event.target.checked)}
        />
      </FilterBar>
      <p className="field-hint board-quick-hint">
        {quickFilterActive(quick) ? t('tasks.board.quick.scope') : t('tasks.board.hint')}
      </p>

      {totalCards === 0 && !quickFilterActive(quick) ? (
        <p className="board-empty" role="status">
          {t('tasks.board.empty')}
        </p>
      ) : null}

      <BoardGrid
        columns={board.columns}
        cards={cards}
        quickFilter={quick}
        me={me}
        now={now}
        lanes={lanes}
        collapsed={collapsed}
        drag={drag}
        draggable={!board.archived && state.pendingMoves === 0}
        loadingMore={state.loadingMore}
        callbacks={callbacks}
        evaluateDrop={evaluateDrop}
        onDropCard={(taskId, columnId, index) => {
          setDrag(null);
          begin(evaluateDrop(taskId, columnId, index));
        }}
        onToggleCollapse={toggleCollapse}
        onLoadMore={state.loadMore}
        gridRef={gridRef}
      />
      {menu.menu}
      <div className="visually-hidden" role="status" aria-live="polite">
        {announcement}
      </div>

      {drawerCard ? (
        <CardDrawer
          card={drawerCard}
          actions={availableActions(drawerCard, can)}
          canAssign={
            can('tasks.manage') &&
            !isTerminal(drawerCard.status) &&
            drawerCard.assignedUserId !== me
          }
          busy={busyCard === drawerCard.id}
          onAction={(action) => runAction(drawerCard, action)}
          onAssign={() => void assignToMe(drawerCard)}
          onClose={() => setDrawerId(undefined)}
        />
      ) : null}

      {dialog?.kind === 'settings' ? (
        <BoardSettingsDialog
          board={board}
          onBoard={(updated) => {
            state.setBoard(updated);
            state.refresh();
          }}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === 'filter' ? (
        <BoardFilterDialog
          board={board}
          onBoard={(updated) => {
            state.setBoard(updated);
            state.reload();
          }}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === 'share' ? (
        <ShareDialog
          view={dialog.view}
          onClose={() => setDialog(null)}
          onChanged={() => {
            viewRead.reload();
            state.refresh();
          }}
        />
      ) : null}
      {dialog?.kind === 'archive' ? (
        <ConfirmDialog
          title={t('tasks.board.archive.title')}
          message={t('tasks.board.archive.message', { name: board.name })}
          confirmLabel={t('tasks.board.action.archive')}
          danger
          onConfirm={() =>
            void archive().catch((cause) => {
              setDialog(null);
              report(cause);
            })
          }
          onCancel={() => setDialog(null)}
        />
      ) : null}
      {dialog?.kind === 'reason' ? (
        <ReasonDialog
          title={t(`tasks.reason.${dialog.action}.title`)}
          label={t('tasks.reason.label')}
          hint={t('tasks.reason.hint')}
          confirmLabel={t(`tasks.action.${dialog.action}`)}
          danger={dialog.action === 'cancel'}
          onSubmit={async (reason) => {
            focusAfter.current = dialog.plan.taskId;
            await execute(dialog.plan, reason);
            setDialog(null);
          }}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </>
  );
}
