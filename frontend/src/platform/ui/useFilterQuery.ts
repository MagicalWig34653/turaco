import { useEffect, useRef } from 'react';
import { mergeFilterQuery, type FilterQueryValues } from './filterQuery';

/** Persist filters already initialized from the URL without creating a history entry per keystroke. */
export function useFilterQuery(values: FilterQueryValues) {
  const pathname = useRef(window.location.pathname);
  const serialized = JSON.stringify(values);
  useEffect(() => {
    if (window.location.pathname !== pathname.current) return;
    const search = mergeFilterQuery(
      window.location.search,
      JSON.parse(serialized) as FilterQueryValues,
    );
    if (search !== window.location.search)
      window.history.replaceState(
        window.history.state,
        '',
        `${pathname.current}${search}${window.location.hash}`,
      );
  }, [serialized]);
}
