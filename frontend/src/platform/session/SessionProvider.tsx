import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { api } from '../api/client';
import { endpoints } from '../api/endpoints';
import type { AuthSession } from '../api/types';
import { clearKerberosSuppression, suppressKerberosAutoLogin } from './kerberosGuard';
import { createCan, type CanFn } from './permissions';

export type SessionState =
  | { status: 'loading' }
  | { status: 'anonymous'; expired: boolean }
  | { status: 'authenticated'; session: AuthSession };

type SessionValue = {
  state: SessionState;
  session: AuthSession | null;
  can: CanFn;
  /** Reloads GET /auth/session, e.g. after a successful login. */
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
};

const SessionContext = createContext<SessionValue | null>(null);

export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<SessionState>({ status: 'loading' });

  const refresh = useCallback(async () => {
    try {
      const session = await endpoints.session();
      clearKerberosSuppression();
      setState({ status: 'authenticated', session });
    } catch {
      setState({ status: 'anonymous', expired: false });
    }
  }, []);

  const logout = useCallback(async () => {
    let redirectUrl: string | undefined;
    try {
      redirectUrl = (await endpoints.logout())?.redirectUrl;
    } catch {
      // The cookie may be unrevokable (network); the local session ends regardless.
    }
    suppressKerberosAutoLogin();
    setState({ status: 'anonymous', expired: false });
    // A shared computer also ends the Microsoft session: the server names the end-session address.
    if (redirectUrl && /^https:\/\/login\.microsoftonline\.com\//.test(redirectUrl)) {
      window.location.assign(redirectUrl);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(
    () =>
      api.onUnauthorized(() => {
        setState((previous) =>
          previous.status === 'authenticated' ? { status: 'anonymous', expired: true } : previous,
        );
      }),
    [],
  );

  const session = state.status === 'authenticated' ? state.session : null;
  const value = useMemo<SessionValue>(
    () => ({ state, session, can: createCan(session), refresh, logout }),
    [state, session, refresh, logout],
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error('useSession must be used inside SessionProvider');
  return value;
}
