import { useEffect, useState } from 'react';
import { api, ApiError, type Query } from '../../api/client';
import { useAsync, usePagedList, type PagedState } from '../../api/useAsync';
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
  list: PagedState<T>;
  state: QueryState;
  setState: (state: QueryState) => void;
  catalog: Catalog | undefined;
  catalogError: ApiError | undefined;
  count: number | undefined;
  countCapped: boolean;
  urlError: boolean;
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
      if (!cursor) {
        setCount(undefined);
        setCountCapped(false);
      }
      if (urlError) throw new ApiError({ status: 400, code: 'query.invalid_filter', message: '' });
      if (catalogRead.data && validate(state, catalogRead.data).length)
        throw new ApiError({ status: 400, code: 'query.invalid_filter', message: '' });
      const filter = extraFilter
        ? {
            v: 1,
            root: state.filter.root
              ? { type: 'group', logic: 'and', children: [state.filter.root, extraFilter] }
              : extraFilter,
          }
        : state.filter;
      type Page = { items: T[]; nextCursor?: string; count?: number; countCapped?: boolean };
      const useCompat = Object.values(quick).some(
        (value) => value !== '' && value !== undefined && value !== null && value !== false,
      );
      const page = useCompat
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
    [resource, serialized, quickKey, extraKey, urlError],
  );
  return {
    list,
    state,
    setState,
    catalog: catalogRead.data,
    catalogError: catalogRead.error,
    count,
    countCapped,
    urlError,
  };
}
