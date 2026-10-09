import type { DragEvent, KeyboardEvent, MouseEvent } from 'react';
import { formatDate } from '../../../platform/format/format';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import { Badge } from '../../../platform/ui/Alert';
import { Avatar } from '../../../platform/ui/Workspace';
import { isOverdue } from '../actions';
import { assigneeLabel } from '../TaskTable';
import type { TaskPriority } from '../types';
import type { KeyboardDirection } from './model';
import type { BoardCard } from './types';

const priorityTone: Record<TaskPriority, 'neutral' | 'info' | 'warning' | 'danger'> = {
  low: 'neutral',
  normal: 'neutral',
  high: 'warning',
  urgent: 'danger',
};

const arrowDirection: Record<string, KeyboardDirection> = {
  ArrowUp: 'up',
  ArrowDown: 'down',
  ArrowLeft: 'left',
  ArrowRight: 'right',
};

export type CardCallbacks = {
  onOpen: (card: BoardCard) => void;
  /** Opens the card menu; `point` is set for a pointer (right click). */
  onMenu: (card: BoardCard, opener: HTMLElement, point?: { x: number; y: number }) => void;
  onDragStart: (card: BoardCard, event: DragEvent<HTMLElement>) => void;
  onDragEnd: () => void;
  /** Alt+Arrow: move the card one slot or column. */
  onKeyboardMove: (card: BoardCard, direction: KeyboardDirection) => void;
  /** Arrow: move focus to a neighbouring card. */
  onFocusMove: (card: BoardCard, direction: KeyboardDirection) => void;
};

/** One Kanban card: title, priority, assignee, due date and (for blocked cards) the reason. */
export function BoardCardView({
  card,
  columnId,
  now,
  draggable,
  dragging,
  dropMark,
  callbacks,
}: {
  card: BoardCard;
  columnId: string;
  now: Date;
  draggable: boolean;
  dragging: boolean;
  dropMark: 'before' | 'after' | undefined;
  callbacks: CardCallbacks;
}) {
  const { t, locale } = useI18n();
  const overdue = isOverdue(card, now);
  const assignee = assigneeLabel(card, '');
  const openMenu = (event: MouseEvent<HTMLElement>) => {
    event.preventDefault();
    callbacks.onMenu(card, event.currentTarget, { x: event.clientX, y: event.clientY });
  };
  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
      event.preventDefault();
      callbacks.onMenu(card, event.currentTarget);
      return;
    }
    const direction = arrowDirection[event.key];
    if (!direction || event.ctrlKey || event.metaKey || event.shiftKey) return;
    event.preventDefault();
    if (event.altKey) callbacks.onKeyboardMove(card, direction);
    else callbacks.onFocusMove(card, direction);
  };
  return (
    <li
      className="board-card"
      data-board-card=""
      data-card-id={card.id}
      data-column-id={columnId}
      data-dragging={dragging || undefined}
      data-drop-mark={dropMark}
      draggable={draggable}
      onDragStart={(event) => callbacks.onDragStart(card, event)}
      onDragEnd={callbacks.onDragEnd}
      onContextMenu={openMenu}
    >
      <button
        type="button"
        className="board-card-main"
        aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown Alt+ArrowLeft Alt+ArrowRight"
        onClick={() => callbacks.onOpen(card)}
        onKeyDown={onKeyDown}
      >
        <span className="board-card-title">{card.title}</span>
        <span className="board-card-meta">
          {card.priority === 'high' || card.priority === 'urgent' ? (
            <Badge tone={priorityTone[card.priority]}>{t(`tasks.priority.${card.priority}`)}</Badge>
          ) : null}
          {card.dueAt ? (
            <span className={overdue ? 'board-card-due board-card-overdue' : 'board-card-due'}>
              {overdue ? `${t('tasks.overdue')} · ` : ''}
              {formatDate(locale, card.dueAt)}
            </span>
          ) : null}
        </span>
        {assignee ? (
          <span className="board-card-assignee">
            <Avatar name={card.assignedUserName ?? card.assignedTeamName} />
            <span>{assignee}</span>
          </span>
        ) : null}
        {card.status === 'blocked' && card.statusReason ? (
          <span className="board-card-reason">{card.statusReason}</span>
        ) : null}
      </button>
      <button
        type="button"
        className="board-card-more"
        aria-haspopup="menu"
        aria-label={t('tasks.board.card.actions', { title: card.title })}
        onClick={(event) => callbacks.onMenu(card, event.currentTarget)}
      >
        <span aria-hidden="true">⋯</span>
      </button>
    </li>
  );
}
