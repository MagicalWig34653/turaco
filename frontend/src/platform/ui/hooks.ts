import { useEffect, useState } from 'react';

export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const handle = window.setTimeout(() => setDebounced(value), delayMs);
    return () => window.clearTimeout(handle);
  }, [value, delayMs]);
  return debounced;
}

/** True when an element's content is wider than its box; tracks resizes of both. */
export function hasHorizontalOverflow(element: Pick<Element, 'scrollWidth' | 'clientWidth'>) {
  return element.scrollWidth - element.clientWidth > 1;
}

export function useHorizontalOverflow<T extends HTMLElement>(
  ref: { current: T | null },
  deps: readonly unknown[] = [],
): boolean {
  const [overflowing, setOverflowing] = useState(false);
  useEffect(() => {
    const element = ref.current;
    if (!element) return;
    const update = () => setOverflowing(hasHorizontalOverflow(element));
    update();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(update);
    observer.observe(element);
    if (element.firstElementChild) observer.observe(element.firstElementChild);
    return () => observer.disconnect();
  }, [ref, ...deps]);
  return overflowing;
}
