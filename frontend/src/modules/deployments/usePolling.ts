import { useEffect, useRef } from 'react';
import { pollIntervalMs } from './runHelpers';

/** Calls `tick` every interval while enabled and the tab is visible; refreshes when it becomes visible again. */
export function usePolling(tick: () => void, enabled: boolean, intervalMs = pollIntervalMs) {
  const ref = useRef(tick);
  ref.current = tick;
  useEffect(() => {
    if (!enabled) return undefined;
    let timer: number | undefined;
    const stop = () => {
      if (timer !== undefined) window.clearInterval(timer);
      timer = undefined;
    };
    const start = () => {
      stop();
      timer = window.setInterval(() => ref.current(), intervalMs);
    };
    const onVisibility = () => {
      if (document.visibilityState === 'hidden') stop();
      else {
        ref.current();
        start();
      }
    };
    if (document.visibilityState !== 'hidden') start();
    document.addEventListener('visibilitychange', onVisibility);
    return () => {
      stop();
      document.removeEventListener('visibilitychange', onVisibility);
    };
  }, [enabled, intervalMs]);
}
