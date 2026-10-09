import { useMemo, useRef, useState } from 'react';
import type { DragEvent, RefObject } from 'react';
import { useI18n } from '../../../platform/i18n/I18nProvider';
import { Badge } from '../../../platform/ui/Alert';
import { Button } from '../../../platform/ui/Button';
import { BoardCardView, type CardCallbacks } from './BoardCardView';
import {
  buildLanes,
  countText,
  matchesQuickFilter,
  slotAfter,
  sortedColumns,
  wipState,
  type CardsState,
  type KeyboardDirection,
  type MoveDecision,
  type MoveRefusal,
  type QuickFilter,
} from './model';
import type { BoardCard, BoardColumn } from './types';

export type DragState = { taskId: string; laneKey: string };

type Preview = {
  columnId: string;
  laneKey: string;
  /** Index among the visible cards of the cell, the moving card excluded. */
  slot: number;
  refusal: MoveRefusal | undefined;
};

export type GridProps = {
  columns: readonly BoardColumn[];
  cards: CardsState;
  quickFilter: QuickFilter;
  me: string | undefined;
  now: Date;
  lanes: boolean;
  collapsed: Readonly<Record<string, boolean>>;
  drag: DragState | null;
  draggable: boolean;
  loadingMore: ReadonlySet<string>;
  callbacks: CardCallbacks;
  onDropCard: (taskId: string, columnId: string, index: number) => void;
  evaluateDrop: (taskId: string, columnId: string, index: number) => MoveDecision;
  onToggleCollapse: (column: BoardColumn) => void;
  onLoadMore: (columnId: string) => void;
  gridRef: RefObject<HTMLDivElement | null>;
};

/** Moves focus to the card above, below or in the neighbouring column of the same lane. */
export function focusNeighbour(root: HTMLElement, from: HTMLElement, direction: KeyboardDirection) {
  const cell = from.closest<HTMLElement>('[data-board-cell]');
  if (!cell) return;
  const here = Array.from(cell.querySelectorAll<HTMLElement>('[data-board-card]'));
  const index = here.findIndex((element) => element === from.closest('[data-board-card]'));
  const focusCard = (element: HTMLElement | undefined) =>
    element?.querySelector<HTMLElement>('.board-card-main')?.focus();
  if (direction === 'up' || direction === 'down') {
    focusCard(here[index + (direction === 'up' ? -1 : 1)]);
    return;
  }
  const step = direction === 'left' ? -1 : 1;
  const lane = cell.dataset.lane ?? '';
  const cells = Array.from(root.querySelectorAll<HTMLElement>('[data-board-cell]')).filter(
    (candidate) => (candidate.dataset.lane ?? '') === lane,
  );
  for (let at = cells.indexOf(cell) + step; at >= 0 && at < cells.length; at += step) {
    const candidates = Array.from(
      cells[at]?.querySelectorAll<HTMLElement>('[data-board-card]') ?? [],
    );
    if (candidates.length > 0) {
      focusCard(candidates[Math.min(Math.max(index, 0), candidates.length - 1)]);
      return;
    }
  }
}

/** Visible slot and its preceding card for a pointer position inside a cell. */
function pointerSlot(cell: HTMLElement, clientY: number, movingId: string) {
  const others = Array.from(cell.querySelectorAll<HTMLElement>('[data-board-card]')).filter(
    (element) => element.dataset.cardId !== movingId,
  );
  let slot = 0;
  for (const element of others) {
    const box = element.getBoundingClientRect();
    if (box.top + box.height / 2 < clientY) slot += 1;
  }
  return { slot, precedingId: others[slot - 1]?.dataset.cardId };
}

export function BoardGrid(props: GridProps) {
  const { t } = useI18n();
  const { cards, drag, lanes: laneMode, collapsed } = props;
  const columns = useMemo(() => sortedColumns({ columns: [...props.columns] }), [props.columns]);
  const [preview, setPreview] = useState<Preview | null>(null);
  const previewRef = useRef<Preview | null>(null);
  const publish = (next: Preview | null) => {
    const old = previewRef.current;
    const same =
      old === next ||
      (old !== null &&
        next !== null &&
        old.columnId === next.columnId &&
        old.laneKey === next.laneKey &&
        old.slot === next.slot &&
        old.refusal === next.refusal);
    if (same) return;
    previewRef.current = next;
    setPreview(next);
  };

  const visible = (columnId: string): BoardCard[] =>
    (cards[columnId]?.items ?? []).filter((card) =>
      matchesQuickFilter(card, props.quickFilter, props.me, props.now),
    );

  const lanes = useMemo(
    () =>
      laneMode
        ? buildLanes(
            Object.fromEntries(
              columns.map((column) => [
                column.id,
                (cards[column.id]?.items ?? []).filter((card) =>
                  matchesQuickFilter(card, props.quickFilter, props.me, props.now),
                ),
              ]),
            ),
            columns.map((column) => column.id),
          )
        : [],
    [laneMode, columns, cards, props.quickFilter, props.me],
  );

  const refusalText = (refusal: MoveRefusal | undefined) =>
    refusal === undefined || refusal === 'noop' || refusal === 'unknown'
      ? ''
      : t(`tasks.board.refusal.${refusal}`);

  const template = columns
    .map((column) => (collapsed[column.id] ? '3.25rem' : 'minmax(15rem, 1fr)'))
    .join(' ');

  const cell = (column: BoardColumn, laneKey: string, items: BoardCard[], laneLabel?: string) => {
    const isCollapsed = collapsed[column.id] === true;
    const full = cards[column.id]?.items ?? [];
    const active = preview?.columnId === column.id && preview.laneKey === laneKey;
    const refused = active && preview.refusal !== undefined && preview.refusal !== 'noop';
    const others = items.filter((item) => item.id !== drag?.taskId);
    const columnIndex = columns.indexOf(column);

    const onDragOver = (event: DragEvent<HTMLElement>) => {
      if (!drag) return;
      if (laneMode && drag.laneKey !== laneKey) {
        event.dataTransfer.dropEffect = 'none';
        return;
      }
      const target = isCollapsed
        ? { slot: 0, precedingId: undefined }
        : pointerSlot(event.currentTarget, event.clientY, drag.taskId);
      const index = slotAfter(full, drag.taskId, target.precedingId);
      const decision = props.evaluateDrop(drag.taskId, column.id, index);
      const refusal = decision.ok ? undefined : decision.refusal;
      publish({ columnId: column.id, laneKey, slot: target.slot, refusal });
      if (decision.ok || refusal === 'noop') {
        event.preventDefault();
        event.dataTransfer.dropEffect = 'move';
      } else {
        event.dataTransfer.dropEffect = 'none';
      }
    };

    const onDrop = (event: DragEvent<HTMLElement>) => {
      if (!drag) return;
      event.preventDefault();
      const target = isCollapsed
        ? { slot: 0, precedingId: undefined }
        : pointerSlot(event.currentTarget, event.clientY, drag.taskId);
      publish(null);
      props.onDropCard(drag.taskId, column.id, slotAfter(full, drag.taskId, target.precedingId));
    };

    const label = laneLabel ? `${column.title} · ${laneLabel}` : column.title;
    return (
      <div
        key={`${laneKey}:${column.id}`}
        className={[
          'board-cell',
          isCollapsed ? 'is-collapsed' : '',
          active && !refused ? 'is-drop-target' : '',
          refused ? 'is-refused' : '',
        ]
          .filter(Boolean)
          .join(' ')}
        data-board-cell=""
        data-column-id={column.id}
        data-column-index={columnIndex}
        data-lane={laneKey}
        onDragOver={onDragOver}
        onDragLeave={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) publish(null);
        }}
        onDrop={onDrop}
      >
        {isCollapsed ? (
          <span className="board-cell-collapsed-note visually-hidden">
            {t('tasks.board.column.collapsedNote', { count: items.length })}
          </span>
        ) : (
          <ul className="board-cards" aria-label={label}>
            {others.length === 0 && items.length === 0 ? (
              <li className="board-cards-empty">{t('tasks.board.column.empty')}</li>
            ) : null}
            {items.map((card) => {
              const index = others.indexOf(card);
              const mark =
                active && !refused && index >= 0 && preview.slot === index
                  ? 'before'
                  : active &&
                      !refused &&
                      index >= 0 &&
                      index === others.length - 1 &&
                      preview.slot === others.length
                    ? 'after'
                    : undefined;
              return (
                <BoardCardView
                  key={card.id}
                  card={card}
                  columnId={column.id}
                  now={props.now}
                  draggable={props.draggable}
                  dragging={drag?.taskId === card.id}
                  dropMark={mark}
                  callbacks={props.callbacks}
                />
              );
            })}
          </ul>
        )}
        {refused ? (
          <p className="board-refusal" role="note">
            {refusalText(preview.refusal)}
          </p>
        ) : null}
      </div>
    );
  };

  return (
    <div className="board-scroll" role="region" aria-label={t('tasks.board.region')}>
      <div
        ref={props.gridRef}
        className="board-grid"
        style={{ gridTemplateColumns: template }}
        onDragEnd={() => publish(null)}
      >
        {columns.map((column) => {
          const bucket = cards[column.id];
          const isCollapsed = collapsed[column.id] === true;
          const wip = bucket ? wipState(bucket) : 'none';
          return (
            <div
              key={`head:${column.id}`}
              className={isCollapsed ? 'board-col-head is-collapsed' : 'board-col-head'}
              data-wip={wip}
            >
              <h2 className="board-col-title">{column.title}</h2>
              {bucket ? (
                <span
                  className="board-col-count"
                  aria-label={t('tasks.board.column.count', { count: countText(bucket) })}
                >
                  {countText(bucket)}
                </span>
              ) : null}
              {bucket && wip !== 'none' && !isCollapsed ? (
                <Badge tone={wip === 'over' ? 'warning' : wip === 'at' ? 'info' : 'neutral'}>
                  {t(
                    wip === 'over'
                      ? 'tasks.board.wip.over'
                      : wip === 'at'
                        ? 'tasks.board.wip.at'
                        : 'tasks.board.wip.ok',
                    { count: bucket.count, limit: bucket.wipLimit ?? 0 },
                  )}
                </Badge>
              ) : null}
              <button
                type="button"
                className="board-col-toggle"
                aria-expanded={!isCollapsed}
                aria-label={t(
                  isCollapsed ? 'tasks.board.column.expand' : 'tasks.board.column.collapse',
                  { title: column.title },
                )}
                onClick={() => props.onToggleCollapse(column)}
              >
                <span aria-hidden="true">{isCollapsed ? '»' : '«'}</span>
              </button>
            </div>
          );
        })}
        {laneMode
          ? lanes.flatMap((lane) => [
              <h3 key={`lane:${lane.key}`} className="board-lane-title">
                {lane.label || t('tasks.assignee.none')}
              </h3>,
              ...columns.map((column) =>
                cell(
                  column,
                  lane.key,
                  lane.cards[column.id] ?? [],
                  lane.label || t('tasks.assignee.none'),
                ),
              ),
            ])
          : columns.map((column) => cell(column, '', visible(column.id)))}
        {columns.map((column) => {
          const bucket = cards[column.id];
          const more = bucket?.nextCursor !== undefined && collapsed[column.id] !== true;
          return (
            <div key={`foot:${column.id}`} className="board-col-foot">
              {more ? (
                <Button
                  busy={props.loadingMore.has(column.id)}
                  onClick={() => props.onLoadMore(column.id)}
                >
                  {t('tasks.board.column.loadMore', { title: column.title })}
                </Button>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
  );
}
