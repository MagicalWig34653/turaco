/** Require continuous visible, focused time before an OAuth approval can be clicked. */
export function watchConsentApproval(
  onChange: (ready: boolean) => void,
  page: Pick<
    Document,
    'visibilityState' | 'hasFocus' | 'addEventListener' | 'removeEventListener'
  > = document,
  browser: Pick<Window, 'addEventListener' | 'removeEventListener'> = window,
): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const refresh = () => {
    if (timer) clearTimeout(timer);
    onChange(false);
    if (page.visibilityState === 'visible' && page.hasFocus()) {
      timer = setTimeout(() => onChange(true), 1500);
    }
  };
  page.addEventListener('visibilitychange', refresh);
  browser.addEventListener('focus', refresh);
  browser.addEventListener('blur', refresh);
  refresh();
  return () => {
    if (timer) clearTimeout(timer);
    page.removeEventListener('visibilitychange', refresh);
    browser.removeEventListener('focus', refresh);
    browser.removeEventListener('blur', refresh);
  };
}
