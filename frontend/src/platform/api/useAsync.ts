import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, isAbortError } from './client';

export type AsyncState<T> = {
  data: T | undefined;
  error: ApiError | undefined;
  loading: boolean;
  reload: () => void;
};

export function asApiError(cause: unknown): ApiError {
  if (cause instanceof ApiError) return cause;
  return new ApiError({
    status: 0,
    code: 'platform.unknown_error',
    message: cause instanceof Error ? cause.message : 'Unknown error',
  });
}

/**
 * Loads data when `deps` change (and on reload()). Local component state only; stale responses
 * are discarded through AbortController.
 */
export function useAsync<T>(
  load: (signal: AbortSignal) => Promise<T>,
  deps: readonly unknown[],
): AsyncState<T> {
  const loadRef = useRef(load);
  loadRef.current = load;
  const [token, setToken] = useState(0);
  const [state, setState] = useState<{
    data: T | undefined;
    error: ApiError | undefined;
    loading: boolean;
  }>({ data: undefined, error: undefined, loading: true });

  useEffect(() => {
    const controller = new AbortController();
    setState((previous) => ({ ...previous, error: undefined, loading: true }));
    loadRef.current(controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) setState({ data, error: undefined, loading: false });
      },
      (cause: unknown) => {
        if (controller.signal.aborted || isAbortError(cause)) return;
        setState({ data: undefined, error: asApiError(cause), loading: false });
      },
    );
    return () => controller.abort();
  }, [...deps, token]);

  const reload = useCallback(() => setToken((value) => value + 1), []);
  return { ...state, reload };
}

export type PagedState<T> = {
  items: T[];
  error: ApiError | undefined;
  loading: boolean;
  loadingMore: boolean;
  loadMoreError: ApiError | undefined;
  hasMore: boolean;
  loadMore: () => void;
  reload: () => void;
};

type PageResult<T> = { items: T[]; nextCursor?: string };

/** Cursor pagination ("load more") on top of the API's nextCursor contract. */
export function usePagedList<T>(
  fetchPage: (cursor: string | undefined, signal: AbortSignal) => Promise<PageResult<T>>,
  deps: readonly unknown[],
): PagedState<T> {
  const fetchRef = useRef(fetchPage);
  fetchRef.current = fetchPage;
  const [token, setToken] = useState(0);
  const [items, setItems] = useState<T[]>([]);
  const [cursor, setCursor] = useState<string | undefined>(undefined);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [loadMoreError, setLoadMoreError] = useState<ApiError | undefined>(undefined);
  const moreController = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    moreController.current?.abort();
    setLoading(true);
    setError(undefined);
    setLoadMoreError(undefined);
    setLoadingMore(false);
    fetchRef.current(undefined, controller.signal).then(
      (page) => {
        if (controller.signal.aborted) return;
        setItems(page.items);
        setCursor(page.nextCursor);
        setLoading(false);
      },
      (cause: unknown) => {
        if (controller.signal.aborted || isAbortError(cause)) return;
        setItems([]);
        setCursor(undefined);
        setError(asApiError(cause));
        setLoading(false);
      },
    );
    return () => controller.abort();
  }, [...deps, token]);

  const loadMore = useCallback(() => {
    if (cursor === undefined) return;
    const controller = new AbortController();
    moreController.current = controller;
    setLoadingMore(true);
    setLoadMoreError(undefined);
    fetchRef.current(cursor, controller.signal).then(
      (page) => {
        if (controller.signal.aborted) return;
        setItems((previous) => [...previous, ...page.items]);
        setCursor(page.nextCursor);
        setLoadingMore(false);
      },
      (cause: unknown) => {
        if (controller.signal.aborted || isAbortError(cause)) return;
        setLoadMoreError(asApiError(cause));
        setLoadingMore(false);
      },
    );
  }, [cursor]);

  useEffect(() => () => moreController.current?.abort(), []);

  const reload = useCallback(() => setToken((value) => value + 1), []);
  return {
    items,
    error,
    loading,
    loadingMore,
    loadMoreError,
    hasMore: cursor !== undefined,
    loadMore,
    reload,
  };
}
