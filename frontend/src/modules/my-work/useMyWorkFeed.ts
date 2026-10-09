import { useState } from 'react';
import { useAsync, usePagedList } from '../../platform/api/useAsync';
import { myWorkApi, type WorkItem } from './api';
import { countViews, dedupeItems, sourcesFor, type SourceFilter } from './feedModel';

/**
 * The merged My Work feed (GET /my-work/items) with cursor paging, the per-source counts
 * (GET /my-work/counts) and the sources that failed for the latest page.
 */
export function useMyWorkFeed(filter: SourceFilter) {
  const [unavailable, setUnavailable] = useState<string[]>([]);
  const list = usePagedList<WorkItem>(
    async (cursor, signal) => {
      const page = await myWorkApi.items({ sources: sourcesFor(filter), cursor }, signal);
      if (!signal.aborted) setUnavailable(page.unavailable ?? []);
      return { items: page.items, ...(page.nextCursor ? { nextCursor: page.nextCursor } : {}) };
    },
    [filter],
  );
  const counts = useAsync((signal) => myWorkApi.counts(undefined, signal), []);
  const reloadList = list.reload;
  const reloadCounts = counts.reload;
  return {
    list,
    items: dedupeItems(list.items),
    unavailable,
    counts: countViews(counts.data?.items),
    countsLoading: counts.loading && !counts.data,
    countsError: counts.error,
    reload: () => {
      reloadList();
      reloadCounts();
    },
  };
}
