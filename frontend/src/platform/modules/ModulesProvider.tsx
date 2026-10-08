import { createContext, useContext, useEffect, useMemo, type ReactNode } from 'react';
import { useAsync } from '../api/useAsync';
import { ApiErrorAlert } from '../ui/ApiErrorAlert';
import { modulesApi } from './api';
import { statusEnabled, type ModuleEnabled } from './model';
const Context = createContext<{ enabled: ModuleEnabled; refresh: () => void; loading: boolean }>({
  enabled: () => false,
  refresh: () => {},
  loading: true,
});
export function ModulesProvider({ children }: { children: ReactNode }) {
  const status = useAsync(modulesApi.status, []);
  const { reload } = status;
  useEffect(() => {
    const timer = window.setInterval(reload, 30000);
    window.addEventListener('focus', reload);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener('focus', reload);
    };
  }, [reload]);
  const enabled = useMemo(() => statusEnabled(status.data?.items), [status.data]);
  return (
    <Context.Provider value={{ enabled, refresh: reload, loading: status.loading && !status.data }}>
      {status.error && <ApiErrorAlert error={status.error} onRetry={reload} />}
      {children}
    </Context.Provider>
  );
}
export const useModules = () => useContext(Context);

/** Unmount embedded feature cards as soon as their module becomes unavailable. */
export function ModuleFeature({ module, children }: { module: string; children: ReactNode }) {
  const { enabled } = useModules();
  return enabled(module) ? children : null;
}
