const KEY = 'turaco.skipKerberos';
let skipInMemory = false;

/** After an explicit logout the login screen must not silently sign the user in again. */
export function suppressKerberosAutoLogin(): void {
  skipInMemory = true;
  try {
    window.sessionStorage.setItem(KEY, '1');
  } catch {
    // in-memory flag still applies
  }
}

export function clearKerberosSuppression(): void {
  skipInMemory = false;
  try {
    window.sessionStorage.removeItem(KEY);
  } catch {
    // ignore
  }
}

export function isKerberosAutoLoginSuppressed(): boolean {
  if (skipInMemory) return true;
  try {
    return window.sessionStorage.getItem(KEY) === '1';
  } catch {
    return false;
  }
}
