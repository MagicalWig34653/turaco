import { afterEach, expect, it, vi } from 'vitest';
import { watchConsentApproval } from './consentDelay';

afterEach(() => vi.useRealTimers());

it('requires 1.5 seconds of continuous visible and focused time', () => {
  vi.useFakeTimers();
  const pageEvents = new EventTarget();
  const browserEvents = new EventTarget();
  let visible = true;
  let focused = true;
  const page = {
    get visibilityState() {
      return visible ? 'visible' : 'hidden';
    },
    hasFocus: () => focused,
    addEventListener: pageEvents.addEventListener.bind(pageEvents),
    removeEventListener: pageEvents.removeEventListener.bind(pageEvents),
  } as Pick<
    Document,
    'visibilityState' | 'hasFocus' | 'addEventListener' | 'removeEventListener'
  >;
  const browser = {
    addEventListener: browserEvents.addEventListener.bind(browserEvents),
    removeEventListener: browserEvents.removeEventListener.bind(browserEvents),
  } as Pick<Window, 'addEventListener' | 'removeEventListener'>;
  let ready = false;
  const stop = watchConsentApproval(
    (value) => {
      ready = value;
    },
    page,
    browser,
  );
  vi.advanceTimersByTime(1499);
  expect(ready).toBe(false);
  visible = false;
  pageEvents.dispatchEvent(new Event('visibilitychange'));
  vi.advanceTimersByTime(2000);
  expect(ready).toBe(false);
  visible = true;
  pageEvents.dispatchEvent(new Event('visibilitychange'));
  vi.advanceTimersByTime(1500);
  expect(ready).toBe(true);
  focused = false;
  browserEvents.dispatchEvent(new Event('blur'));
  expect(ready).toBe(false);
  focused = true;
  browserEvents.dispatchEvent(new Event('focus'));
  vi.advanceTimersByTime(1500);
  expect(ready).toBe(true);
  stop();
});
