import { useCallback, useEffect, useRef, useState } from 'react';
import { isAbortError, type ApiError } from '../../../platform/api/client';
import { asApiError } from '../../../platform/api/useAsync';
import { tasksApi } from '../api';
import type { Task } from '../types';
import { boardsApi } from './api';
import {
  appendPage,
  applyMove,
  cardsFromResponse,
  findCard,
  mergeRefresh,
  replaceCard,
  revertMove,
  setRank,
  settleMove,
  shouldRefetchAfter,
  type CardsState,
  type MovePlan,
} from './model';
import type { TaskBoard } from './types';

const POLL_MS = 30_000;
const MIN_REFRESH_GAP_MS = 4_000;

export type BoardState = {
  board: TaskBoard | undefined;
  cards: CardsState | undefined;
  error: ApiError | undefined;
  /** A background refresh failed; the shown cards may be stale. */
  refreshFailed: boolean;
  loading: boolean;
  loadingMore: ReadonlySet<string>;
  loadMoreError: ApiError | undefined;
  /** Moves currently waiting for the server. */
  pendingMoves: number;
  reload: () => void;
  /** Refetches quietly (focus, polling, after a conflict). */
  refresh: () => void;
  setBoard: (board: TaskBoard) => void;
  loadMore: (columnId: string) => void;
  /** Applies the move at once and rolls it back (rethrowing) when the server refuses it. */
  moveCard: (plan: MovePlan, reason?: string) => Promise<void>;
  patchTask: (task: Task) => void;
};

/**
 * Loads a Board and its cards, refetches on focus and by polling (paused while `paused`), and runs
 * card moves optimistically. Rollback uses the inverse of the move so overlapping moves survive.
 */
export function useBoard(id: string, paused: boolean): BoardState {
  const [board, setBoardState] = useState<TaskBoard>();
  const [cards, setCards] = useState<CardsState>();
  const [error, setError] = useState<ApiError>();
  const [refreshFailed, setRefreshFailed] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState<ReadonlySet<string>>(new Set());
  const [loadMoreError, setLoadMoreError] = useState<ApiError>();
  const [pendingMoves, setPendingMoves] = useState(0);
  const [token, setToken] = useState(0);
  const cardsRef = useRef<CardsState | undefined>(undefined);
  cardsRef.current = cards;
  const pausedRef = useRef(paused);
  pausedRef.current = paused;
  const inflight = useRef(0);
  const paged = useRef(new Set<string>());
  const refreshing = useRef<AbortController | undefined>(undefined);
  const lastRefresh = useRef(0);

  const fetchAll = useCallback(
    async (signal: AbortSignal) => {
      const [nextBoard, page] = await Promise.all([
        boardsApi.get(id, signal),
        boardsApi.cards(id, {}, signal),
      ]);
      return { nextBoard, fresh: cardsFromResponse(page.columns) };
    },
    [id],
  );

  // Initial and explicit loads.
  useEffect(() => {
    const controller = new AbortController();
    paged.current = new Set();
    setLoading(true);
    setError(undefined);
    fetchAll(controller.signal).then(
      ({ nextBoard, fresh }) => {
        if (controller.signal.aborted) return;
        setBoardState(nextBoard);
        setCards(fresh);
        setRefreshFailed(false);
        setLoading(false);
      },
      (cause: unknown) => {
        if (controller.signal.aborted || isAbortError(cause)) return;
        setError(asApiError(cause));
        setLoading(false);
      },
    );
    return () => controller.abort();
  }, [fetchAll, token]);

  const refresh = useCallback(() => {
    if (inflight.current > 0 || pausedRef.current) return;
    const now = Date.now();
    if (now - lastRefresh.current < MIN_REFRESH_GAP_MS) return;
    lastRefresh.current = now;
    refreshing.current?.abort();
    const controller = new AbortController();
    refreshing.current = controller;
    fetchAll(controller.signal).then(
      ({ nextBoard, fresh }) => {
        // A move that started meanwhile owns the cards until it settles.
        if (controller.signal.aborted || inflight.current > 0) return;
        setBoardState(nextBoard);
        setCards((previous) => (previous ? mergeRefresh(previous, fresh, paged.current) : fresh));
        setRefreshFailed(false);
      },
      (cause: unknown) => {
        if (controller.signal.aborted || isAbortError(cause)) return;
        setRefreshFailed(true);
      },
    );
  }, [fetchAll]);

  useEffect(() => {
    const onFocus = () => {
      if (document.visibilityState === 'visible') refresh();
    };
    const timer = window.setInterval(onFocus, POLL_MS);
    window.addEventListener('focus', onFocus);
    document.addEventListener('visibilitychange', onFocus);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener('focus', onFocus);
      document.removeEventListener('visibilitychange', onFocus);
      refreshing.current?.abort();
    };
  }, [refresh]);

  const loadMore = useCallback(
    (columnId: string) => {
      const cursor = cardsRef.current?.[columnId]?.nextCursor;
      if (!cursor) return;
      setLoadingMore((previous) => new Set(previous).add(columnId));
      setLoadMoreError(undefined);
      boardsApi.cards(id, { column: columnId, cursor }).then(
        (page) => {
          paged.current.add(columnId);
          setCards((previous) =>
            previous
              ? page.columns.reduce((state, next) => appendPage(state, next), previous)
              : previous,
          );
          setLoadingMore((previous) => {
            const next = new Set(previous);
            next.delete(columnId);
            return next;
          });
        },
        (cause: unknown) => {
          setLoadMoreError(asApiError(cause));
          setLoadingMore((previous) => {
            const next = new Set(previous);
            next.delete(columnId);
            return next;
          });
        },
      );
    },
    [id],
  );

  const moveCard = useCallback(
    async (plan: MovePlan, reason?: string) => {
      const original = cardsRef.current ? findCard(cardsRef.current, plan.taskId)?.card : undefined;
      if (!original) return;
      inflight.current += 1;
      setPendingMoves((value) => value + 1);
      setCards((previous) => (previous ? applyMove(previous, plan) : previous));
      try {
        if (plan.toColumnId === null) {
          if (!plan.action) return;
          const task = await tasksApi.transition(
            plan.taskId,
            plan.action,
            plan.expectedVersion,
            reason,
          );
          setCards((previous) => previous && settleMove(previous, task.id, task, null));
        } else if (plan.action === null) {
          const placed = await boardsApi.place(id, {
            taskId: plan.taskId,
            columnId: plan.toColumnId,
            anchor: plan.anchor ?? { top: true },
          });
          setCards((previous) => previous && setRank(previous, plan.taskId, placed.rank));
        } else {
          const result = await boardsApi.move(id, {
            taskId: plan.taskId,
            columnId: plan.toColumnId,
            expectedVersion: plan.expectedVersion,
            reason,
            anchor: plan.anchor,
          });
          setCards(
            (previous) => previous && settleMove(previous, plan.taskId, result.task, result.rank),
          );
        }
      } catch (cause) {
        setCards((previous) => previous && revertMove(previous, plan, original));
        const failure = asApiError(cause);
        if (shouldRefetchAfter(failure.code)) lastRefresh.current = 0;
        throw failure;
      } finally {
        inflight.current -= 1;
        setPendingMoves((value) => value - 1);
        if (inflight.current === 0) {
          lastRefresh.current = 0;
          window.setTimeout(refresh, 400);
        }
      }
    },
    [id, refresh],
  );

  return {
    board,
    cards,
    error,
    refreshFailed,
    loading,
    loadingMore,
    loadMoreError,
    pendingMoves,
    reload: () => setToken((value) => value + 1),
    refresh: () => {
      lastRefresh.current = 0;
      refresh();
    },
    setBoard: setBoardState,
    loadMore,
    moveCard,
    patchTask: (task) =>
      setCards((previous) => (previous ? replaceCard(previous, task) : previous)),
  };
}
