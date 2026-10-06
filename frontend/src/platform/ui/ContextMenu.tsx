import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { createPortal } from 'react-dom';
import './ContextMenu.css';

export type MenuItem =
  | {
      id: string;
      label: string;
      icon?: ReactNode;
      shortcut?: string;
      disabledReason?: string;
      danger?: boolean;
      onSelect: () => void;
    }
  | { id: string; separator: true };

export type MenuPosition = { x: number; y: number };

type MenuState = {
  items: readonly MenuItem[];
  position: MenuPosition;
  opener: HTMLElement;
  label: string;
};

/** Clamp a fixed-position menu within the visual viewport. */
export function positionContextMenu(
  point: MenuPosition,
  size: { width: number; height: number },
  viewport: { width: number; height: number },
  margin = 8,
): MenuPosition {
  return {
    x: Math.max(margin, Math.min(point.x, viewport.width - size.width - margin)),
    y: Math.max(margin, Math.min(point.y, viewport.height - size.height - margin)),
  };
}

export function nextMenuIndex(current: number, length: number, key: string): number {
  if (length === 0) return -1;
  if (key === 'Home') return 0;
  if (key === 'End') return length - 1;
  if (current < 0) return key === 'ArrowUp' ? length - 1 : 0;
  if (key === 'ArrowDown') return (current + 1 + length) % length;
  if (key === 'ArrowUp') return (current - 1 + length) % length;
  return current;
}

/** Returns false when clipboard access is unavailable, so callers can offer selectable text. */
export async function copyContextText(value: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    return false;
  }
}

export function useContextMenu() {
  const [menu, setMenu] = useState<MenuState | null>(null);

  const openAtPoint = (
    items: readonly MenuItem[],
    position: MenuPosition,
    opener: HTMLElement,
    label: string,
  ) => {
    if (!items.some((item) => !('separator' in item))) return;
    setMenu({ items, position, opener, label });
  };

  const openAtElement = (items: readonly MenuItem[], opener: HTMLElement, label: string) => {
    const bounds = opener.getBoundingClientRect();
    openAtPoint(items, { x: bounds.left, y: bounds.bottom + 4 }, opener, label);
  };

  const close = (restoreFocus = false) => {
    const opener = menu?.opener;
    setMenu(null);
    if (restoreFocus && opener?.isConnected) opener.focus({ preventScroll: true });
  };

  return {
    openAtPoint,
    openAtElement,
    close,
    menu: menu ? <ContextMenu state={menu} onClose={close} /> : null,
  };
}

function ContextMenu({
  state,
  onClose,
}: {
  state: MenuState;
  onClose: (restoreFocus?: boolean) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState<MenuPosition>(state.position);

  useLayoutEffect(() => {
    const element = ref.current;
    if (!element) return;
    const bounds = element.getBoundingClientRect();
    setPosition(
      positionContextMenu(
        state.position,
        { width: bounds.width, height: bounds.height },
        { width: window.innerWidth, height: window.innerHeight },
      ),
    );
    element.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')?.focus({
      preventScroll: true,
    });
  }, [state]);

  useEffect(() => {
    const dismissOnPointer = (event: PointerEvent) => {
      if (!ref.current?.contains(event.target as Node)) onClose(false);
    };
    const dismissOnScroll = (event: Event) => {
      if (!ref.current?.contains(event.target as Node)) onClose(false);
    };
    const dismissOnResize = () => onClose(false);
    document.addEventListener('pointerdown', dismissOnPointer, true);
    document.addEventListener('scroll', dismissOnScroll, true);
    window.addEventListener('resize', dismissOnResize);
    return () => {
      document.removeEventListener('pointerdown', dismissOnPointer, true);
      document.removeEventListener('scroll', dismissOnScroll, true);
      window.removeEventListener('resize', dismissOnResize);
    };
  }, [onClose]);

  const dismissAndContinueTab = (backward: boolean) => {
    const focusable = Array.from(
      document.querySelectorAll<HTMLElement>(
        'a[href], button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      ),
    ).filter((element) => !ref.current?.contains(element) && element.getClientRects().length > 0);
    const index = focusable.indexOf(state.opener);
    const next = focusable[index + (backward ? -1 : 1)];
    onClose(false);
    (next ?? state.opener).focus({ preventScroll: true });
  };

  return createPortal(
    <div
      ref={ref}
      className="context-menu"
      role="menu"
      aria-label={state.label}
      style={{ left: position.x, top: position.y }}
      onKeyDown={(event) => {
        const choices = Array.from(
          ref.current?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') ??
            [],
        );
        if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
          event.preventDefault();
          const current = choices.indexOf(document.activeElement as HTMLButtonElement);
          choices[nextMenuIndex(current, choices.length, event.key)]?.focus();
        } else if (event.key === 'Escape') {
          event.preventDefault();
          onClose(true);
        } else if (event.key === 'Tab') {
          event.preventDefault();
          dismissAndContinueTab(event.shiftKey);
        }
      }}
    >
      {state.items.map((item) =>
        'separator' in item ? (
          <div key={item.id} className="context-menu-separator" role="separator" />
        ) : (
          <button
            key={item.id}
            type="button"
            role="menuitem"
            className={
              item.danger ? 'context-menu-item context-menu-item-danger' : 'context-menu-item'
            }
            disabled={Boolean(item.disabledReason)}
            title={item.disabledReason}
            onClick={() => {
              onClose(true);
              item.onSelect();
            }}
          >
            {item.icon ? (
              <span className="context-menu-icon" aria-hidden="true">
                {item.icon}
              </span>
            ) : null}
            <span className="context-menu-label">{item.label}</span>
            {item.shortcut ? (
              <span className="context-menu-shortcut" aria-hidden="true">
                {item.shortcut}
              </span>
            ) : null}
          </button>
        ),
      )}
    </div>,
    document.body,
  );
}
