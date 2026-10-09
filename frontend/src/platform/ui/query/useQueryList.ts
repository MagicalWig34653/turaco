import { useEffect, useState } from 'react';
import { api, ApiError, type Query } from '../../api/client';
import { asApiError, useAsync, usePagedList, type PagedState } from '../../api/useAsync';
import { navigate } from '../../router/Router';
import { viewsApi, type SavedView, type ViewWarning } from '../views/api';
import { definitionToState, sameQuery } from '../views/model';
import {
  deserialize,
  emptyState,
  serialize,
  validate,
  type Catalog,
  type Node,
  type QueryState,
} from './filterModel';

export type QueryList<T> = {
  resource: 'tickets' | 'devices' | 'tasks';
  list: PagedState<T>;
  state: QueryState;
  setState: (state: QueryState) => void;
  catalog: Catalog | undefined;
  catalogError: ApiError | undefined;
  count: number | undefined;
  countCapped: boolean;
  urlError: boolean;
  /** The Saved View this list was opened from (`?view=<id>`), if any. */
  view: SavedView | undefined;
  viewPending: boolean;
  viewError: ApiError | undefined;
  /** Counter that grows whenever a View was applied (opened or loaded from the URL). */
  viewApplied: number;
  /** `query.field_unavailable` warnings of the View for the current viewer. */
  warnings: ViewWarning[];
  /** Applies the View's filter, sort and search and writes it to the URL. */
  openView: (view: SavedView) => void;
  /** Leaves the View but keeps the working filter as an ad-hoc query. */
  closeView: () => void;
  /** Replaces the stored View after a save, rename or share change without touching the filter. */
  refreshView: (view: SavedView) => void;
};
export function useQueryList<T>(
  resource: 'tickets' | 'devices' | 'tasks',
  quick: Query = {},
  extraFilter?: Node,
): QueryList<T> {
  const [initial] = useState(() => {
    const raw = new URLSearchParams(window.location.search).get('query');
    try {
      return { state: raw ? deserialize(raw) : emptyState(), error: false };
    } catch {
      return { state: emptyState(), error: true };
    }
  });
  const [state, update] = useState(initial.state);
  const [urlError, setUrlError] = useState(initial.error);
  const [initialView] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    return { id: params.get('view'), hasQuery: params.has('query') };
  });
  const [view, setView] = useState<SavedView>();
  const [viewPending, setViewPending] = useState(initialView.id !== null);
  const [viewError, setViewError] = useState<ApiError>();
  const [viewApplied, setViewApplied] = useState(0);
  const [warnings, setWarnings] = useState<ViewWarning[]>([]);
  const [count, setCount] = useState<number>();
  const [countCapped, setCountCapped] = useState(false);
  const catalogRead = useAsync(
    (signal) => api.get<Catalog>(`/${resource}/fields`, { signal }),
    [resource],
  );
  const serialized = serialize(state);
  const quickKey = JSON.stringify(quick);
  const extraKey = JSON.stringify(extraFilter);
  const setState = (next: QueryState) => {
    update(next);
    setUrlError(false);
    const url = new URL(window.location.href);
    url.searchParams.set('query', serialize(next));
    window.history.replaceState(window.history.state, '', url);
  };
  const writeUrl = (next: QueryState | undefined, viewId: string | null) => {
    const url = new URL(window.location.href);
    if (next) url.searchParams.set('query', serialize(next));
    if (viewId === null) url.searchParams.delete('view');
    else url.searchParams.set('view', viewId);
    navigate(`${url.pathname}${url.search}${url.hash}`, { replace: true });
  };
  const openView = (opened: SavedView) => {
    const next = definitionToState(opened.definition);
    setView(opened);
    setViewError(undefined);
    update(next);
    setUrlError(false);
    setViewApplied((value) => value + 1);
    writeUrl(next, opened.id);
  };
  const closeView = () => {
    setView(undefined);
    setWarnings([]);
    writeUrl(undefined, null);
  };
  useEffect(() => {
    const id = initialView.id;
    if (!id) return;
    const controller = new AbortController();
    viewsApi.get(id, controller.signal).then(
      (loaded) => {
        if (controller.signal.aborted) return;
        if (loaded.resource !== resource) {
          setViewError(new ApiError({ status: 404, code: 'views.not_found', message: '' }));
          writeUrl(undefined, null);
        } else {
          setView(loaded);
          setViewApplied((value) => value + 1);
          if (!initialView.hasQuery) {
            const next = definitionToState(loaded.definition);
            update(next);
            writeUrl(next, id);
          }
        }
        setViewPending(false);
      },
      (cause: unknown) => {
        if (controller.signal.aborted) return;
        setViewError(asApiError(cause));
        writeUrl(undefined, null);
        setViewPending(false);
      },
    );
    return () => controller.abort();
    // The URL is read once at mount.
  }, []);
  const viewRunnable = view !== undefined && view.access !== 'admin' && view.moduleEnabled;
  const viewKey = view ? `${view.id}:${view.version}` : '';
  useEffect(() => {
    if (!view || !viewRunnable) {
      setWarnings([]);
      return;
    }
    const controller = new AbortController();
    viewsApi.run(view.id, { limit: 1 }, controller.signal).then(
      (page) => {
        if (!controller.signal.aborted) setWarnings(page.warnings ?? []);
      },
      () => {
        if (!controller.signal.aborted) setWarnings([]);
      },
    );
    return () => controller.abort();
    // Keyed on the View identity and version, not on every render of the object.
  }, [viewKey, viewRunnable]);
  useEffect(() => {
    const restore = () => {
      try {
        update(
          deserialize(
            new URLSearchParams(window.location.search).get('query') ?? serialize(emptyState()),
          ),
        );
        setUrlError(false);
      } catch {
        setUrlError(true);
      }
    };
    window.addEventListener('popstate', restore);
    return () => window.removeEventListener('popstate', restore);
  }, []);
  const list = usePagedList<T>(
    async (cursor, signal) => {
      if (viewPending) {
        // Wait for the View to resolve; the dependency change below cancels this request.
        await new Promise<void>((resolve) => signal.addEventListener('abort', () => resolve()));
        return { items: [] };
      }
      if (!cursor) {
        setCount(undefined);
        setCountCapped(false);
      }
      type Page = { items: T[]; nextCursor?: string; count?: number; countCapped?: boolean };
      const useCompat = Object.values(quick).some(
        (value) => value !== '' && value !== undefined && value !== null && value !== false,
      );
      // An untouched View runs through its own endpoint, so conditions the viewer may not use
      // match nothing (reported as warnings) instead of failing the whole request.
      const runView =
        view !== undefined &&
        viewRunnable &&
        !useCompat &&
        !extraFilter &&
        sameQuery(state, definitionToState(view.definition))
          ? view
          : undefined;
      if (urlError) throw new ApiError({ status: 400, code: 'query.invalid_filter', message: '' });
      if (!runView && catalogRead.data && validate(state, catalogRead.data).length)
        throw new ApiError({ status: 400, code: 'query.invalid_filter', message: '' });
      const filter = extraFilter
        ? {
            v: 1,
            root: state.filter.root
              ? { type: 'group', logic: 'and', children: [state.filter.root, extraFilter] }
              : extraFilter,
          }
        : state.filter;
      const page = runView
        ? await viewsApi.run<T>(runView.id, { cursor, limit: 50, count: true }, signal)
        : useCompat
          ? await api.get<Page>(`/${resource}`, {
              signal,
              query: {
                ...quick,
                filter: JSON.stringify(filter),
                sort: state.sort.map((s) => `${s.field}:${s.dir}:${s.nulls ?? 'last'}`).join(','),
                search: state.search,
                count: true,
                limit: 50,
                cursor,
              },
            })
          : await api.post<Page>(
              `/${resource}/query`,
              { filter, sort: state.sort, search: state.search, count: true, limit: 50, cursor },
              { signal },
            );
      if (!signal.aborted && !cursor) {
        setCount(page.count);
        setCountCapped(page.countCapped ?? false);
      }
      return { items: page.items, ...(page.nextCursor ? { nextCursor: page.nextCursor } : {}) };
    },
    [resource, serialized, quickKey, extraKey, urlError, viewPending, viewKey],
  );
  return {
    resource,
    list,
    state,
    setState,
    catalog: catalogRead.data,
    catalogError: catalogRead.error,
    count,
    countCapped,
    urlError,
    view,
    viewPending,
    viewError,
    viewApplied,
    warnings,
    openView,
    closeView,
    refreshView: setView,
  };
}
