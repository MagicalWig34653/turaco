import { useSyncExternalStore } from 'react';
import type { AnchorHTMLAttributes, MouseEvent, ReactNode } from 'react';

const LOCATION_EVENT = 'turaco:navigate';

function subscribe(callback: () => void): () => void {
  window.addEventListener('popstate', callback);
  window.addEventListener(LOCATION_EVENT, callback);
  return () => {
    window.removeEventListener('popstate', callback);
    window.removeEventListener(LOCATION_EVENT, callback);
  };
}

function snapshot(): string {
  return `${window.location.pathname}${window.location.search}`;
}

export type Location = { pathname: string; search: string };

/** History API based location; re-renders on navigate() and back/forward. */
export function useLocation(): Location {
  const current = useSyncExternalStore(subscribe, snapshot);
  const index = current.indexOf('?');
  return index === -1
    ? { pathname: current, search: '' }
    : { pathname: current.slice(0, index), search: current.slice(index) };
}

export function navigate(to: string, options: { replace?: boolean } = {}): void {
  if (options.replace) window.history.replaceState(null, '', to);
  else window.history.pushState(null, '', to);
  window.dispatchEvent(new Event(LOCATION_EVENT));
}

type LinkProps = Omit<AnchorHTMLAttributes<HTMLAnchorElement>, 'href'> & {
  to: string;
  children: ReactNode;
};

export function Link({ to, children, onClick, target, ...rest }: LinkProps) {
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    onClick?.(event);
    if (
      event.defaultPrevented ||
      event.button !== 0 ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey ||
      (target && target !== '_self')
    ) {
      return;
    }
    event.preventDefault();
    navigate(to);
  };
  return (
    <a {...rest} href={to} onClick={handleClick} {...(target ? { target } : {})}>
      {children}
    </a>
  );
}
