import { useModules } from '../../platform/modules/ModulesProvider';
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react';
import { useSession } from '../../platform/session/SessionProvider';
import { presenceApi } from './api';
import { presenceCan } from './model';
import type { PresenceStatus } from './types';
const Context = createContext<{ status: PresenceStatus | undefined; refresh: () => void }>({
  status: undefined,
  refresh: () => {},
});
export function PresenceProvider({ children }: { children: ReactNode }) {
  const { session } = useSession();
  const userId = session?.userId;
  const [snapshot, setSnapshot] = useState<{
    userId: string | undefined;
    status: PresenceStatus;
  }>();
  const [generation, setGeneration] = useState(0);
  const refresh = useCallback(() => {
    setSnapshot(undefined);
    setGeneration((n) => n + 1);
  }, []);
  useEffect(() => {
    let controller: AbortController;
    const load = () => {
      controller?.abort();
      const request = new AbortController();
      controller = request;
      void presenceApi.status(request.signal).then(
        (status) => {
          if (!request.signal.aborted) setSnapshot({ userId, status });
        },
        () => {
          if (!request.signal.aborted) setSnapshot(undefined);
        },
      );
    };
    load();
    const timer = window.setInterval(load, 60_000);
    window.addEventListener('focus', load);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      window.removeEventListener('focus', load);
    };
  }, [userId, generation]);
  const status = snapshot?.userId === userId ? snapshot?.status : undefined;
  return <Context.Provider value={{ status, refresh }}>{children}</Context.Provider>;
}
export function usePresence() {
  const context = useContext(Context);
  const { can } = useSession();
  const { enabled, refresh: refreshModules } = useModules();
  const refresh = () => {
    context.refresh();
    refreshModules();
  };
  const gatedCan = useMemo(
    () =>
      presenceCan(
        context.status
          ? { ...context.status, enabled: context.status.enabled && enabled('presence') }
          : undefined,
        can,
      ),
    [context.status, can, enabled],
  );
  return {
    ...context,
    status: context.status
      ? { ...context.status, enabled: context.status.enabled && enabled('presence') }
      : undefined,
    refresh,
    can: gatedCan,
  };
}
