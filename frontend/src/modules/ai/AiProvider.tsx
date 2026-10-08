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
import { usePresence } from '../presence/PresenceProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { Button } from '../../platform/ui/Button';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { aiApi } from './api';
import { aiCan } from './model';
import type { ResourceRef, Status } from './types';
import { AssistantPanel } from './AssistantPanel';
import './ai.css';
const Context = createContext<{
  status?: Status | undefined;
  refresh: () => void;
  open: (context?: ResourceRef) => void;
}>({ refresh: () => {}, open: () => {} });
export function AiProvider({ children }: { children: ReactNode }) {
  const { session } = useSession();
  const { enabled } = useModules();
  const [status, setStatus] = useState<Status>();
  const [generation, setGeneration] = useState(0);
  const [panel, setPanel] = useState<{ context?: ResourceRef | undefined; sequence: number }>();
  const [visible, setVisible] = useState(false);
  const refresh = useCallback(() => setGeneration((n) => n + 1), []);
  const open = useCallback((context?: ResourceRef) => {
    setPanel((old) => ({ context, sequence: (old?.sequence ?? 0) + 1 }));
    setVisible(true);
  }, []);
  useEffect(() => {
    let controller: AbortController;
    const load = () => {
      controller?.abort();
      controller = new AbortController();
      const request = controller;
      void aiApi.status(request.signal).then(
        (value) => {
          if (!request.signal.aborted) setStatus(value);
        },
        () => {
          if (!request.signal.aborted) setStatus(undefined);
        },
      );
    };
    load();
    const timer = window.setInterval(load, 60000);
    window.addEventListener('focus', load);
    return () => {
      controller.abort();
      window.clearInterval(timer);
      window.removeEventListener('focus', load);
    };
  }, [session?.userId, generation]);
  return (
    <Context.Provider value={{ status, refresh, open }}>
      {children}
      {enabled('ai') && status?.enabled && status.permissions.use && (
        <AssistantPanel
          key={session?.userId}
          status={status}
          visible={visible}
          context={panel?.context}
          sequence={panel?.sequence ?? 0}
          onClose={() => setVisible(false)}
        />
      )}
    </Context.Provider>
  );
}
export function useAi() {
  const context = useContext(Context);
  const { can } = usePresence();
  const { enabled, refresh: refreshModules } = useModules();
  const refresh = () => {
    context.refresh();
    refreshModules();
  };
  const gated = useMemo(
    () =>
      aiCan(
        context.status
          ? { ...context.status, enabled: context.status.enabled && enabled('ai') }
          : undefined,
        can,
      ),
    [context.status, can, enabled],
  );
  return { ...context, refresh, can: gated };
}
export function AskTuraco({ context }: { context?: ResourceRef }) {
  const { can, open } = useAi();
  const { t } = useI18n();
  return can('ai.use') ? <Button onClick={() => open(context)}>{t('ai.ask')}</Button> : null;
}
