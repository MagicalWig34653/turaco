import { useCallback, useEffect, useId, useRef, useState } from 'react';
import { useI18n } from '../platform/i18n/I18nProvider';
import { Link, navigate, useLocation } from '../platform/router/Router';
import { useContextMenu, type MenuItem } from '../platform/ui/ContextMenu';
import { NavIcon } from '../platform/ui/NavIcon';
import {
  notifySidebarChanged,
  onSidebarChanged,
  viewsApi,
  type SidebarResponse,
  type ViewPin,
} from '../platform/ui/views/api';
import { movePin, pinPayload, removePin } from '../platform/ui/views/model';
import type { MessageKey } from '../platform/i18n/i18n';
import type { RouteId } from './routes';
import type { NavSection } from './shellNavigation';
import {
  PINNED_KEY,
  countLabel,
  pinnedActiveOn,
  pinnedItems,
  readCollapsedSections,
  sanitizeCollapsed,
  toggleSection,
  writeCollapsedSections,
  type PinnedItem,
} from './sidebarModel';

const resourceIcon: Record<string, RouteId> = {
  tickets: 'ticketQueue',
  devices: 'devices',
  tasks: 'tasks',
};

type PinChange = (items: ViewPin[]) => { items: ViewPin[]; touched?: Set<string> } | null;

type Props = {
  sections: NavSection[];
  /** Icon-only rail mode: headings and counts are hidden, every destination stays reachable. */
  rail: boolean;
  /** Pattern of the route that is current, as decided by the shell. */
  activePath: string | undefined;
  unreadLabel: string | null;
  unreadAria: string | null;
};

/**
 * Grouped, collapsible navigation with a "Pinned" section for the caller's Saved Views.
 * Destinations arrive already filtered by permission and module status; this component only presents them.
 */
export function Sidebar({ sections, rail, activePath, unreadLabel, unreadAria }: Props) {
  const { t } = useI18n();
  const { pathname, search } = useLocation();
  const menu = useContextMenu();
  const navRef = useRef<HTMLElement>(null);
  const baseId = useId();
  const [collapsed, setCollapsed] = useState(readCollapsedSections);
  const [sidebar, setSidebar] = useState<SidebarResponse>();
  const [pinError, setPinError] = useState(false);
  const pinned = pinnedItems(sidebar);
  const activePin = pinnedActiveOn(pinned, pathname, search);

  const load = useCallback((signal?: AbortSignal) => {
    viewsApi.sidebar(signal).then(
      (response) => {
        if (signal?.aborted) return;
        setSidebar(response);
        const remote = sanitizeCollapsed(response.collapsedGroups);
        setCollapsed((local) => {
          if (remote.length > 0 || local.length === 0) {
            writeCollapsedSections(remote);
            return remote;
          }
          // Server has no preference yet: keep the browser's and store it.
          viewsApi.setSidebarState(local).catch(() => undefined);
          return local;
        });
      },
      () => {
        // The pinned section is optional: an older API or a transient error leaves it out.
      },
    );
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal);
    const off = onSidebarChanged(() => load());
    return () => {
      controller.abort();
      off();
    };
  }, [load]);

  const toggle = (key: string) => {
    const next = toggleSection(collapsed, key);
    setCollapsed(next);
    writeCollapsedSections(next);
    viewsApi.setSidebarState(next).catch(() => undefined);
  };

  const changePins = async (change: PinChange) => {
    setPinError(false);
    try {
      const current = await viewsApi.pins();
      const result = change(current.items);
      if (!result) return;
      await viewsApi.replacePins(pinPayload(result.items, result.touched));
      notifySidebarChanged();
    } catch {
      setPinError(true);
    }
  };

  const pinActions = (item: PinnedItem): MenuItem[] => {
    const group = pinned.filter((candidate) => candidate.groupKey === item.groupKey);
    const index = group.findIndex((candidate) => candidate.viewId === item.viewId);
    const first = index <= 0;
    const last = index === group.length - 1;
    return [
      { id: 'open', label: t('sidebar.pin.open'), onSelect: () => navigate(item.href) },
      first
        ? {
            id: 'up',
            label: t('sidebar.pin.moveUp'),
            disabledReason: t('sidebar.pin.atStart'),
            onSelect: () => undefined,
          }
        : {
            id: 'up',
            label: t('sidebar.pin.moveUp'),
            onSelect: () => void changePins((items) => movePin(items, item.viewId, -1)),
          },
      last
        ? {
            id: 'down',
            label: t('sidebar.pin.moveDown'),
            disabledReason: t('sidebar.pin.atEnd'),
            onSelect: () => undefined,
          }
        : {
            id: 'down',
            label: t('sidebar.pin.moveDown'),
            onSelect: () => void changePins((items) => movePin(items, item.viewId, 1)),
          },
      { id: 'sep', separator: true },
      {
        id: 'unpin',
        label: item.source === 'rule' ? t('sidebar.pin.hide') : t('sidebar.pin.unpin'),
        danger: true,
        onSelect: () => void changePins((items) => ({ items: removePin(items, item.viewId) })),
      },
    ];
  };

  const onKeyDown = (event: React.KeyboardEvent<HTMLElement>) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const stops = [
      ...(navRef.current?.querySelectorAll<HTMLElement>(
        'a[href], .sidebar-section-toggle, .sidebar-pin-more',
      ) ?? []),
    ].filter(
      (element) => element.getClientRects().length > 0 && !element.matches('.sidebar-pin-more'),
    );
    const index = stops.indexOf(document.activeElement as HTMLElement);
    if (index < 0) return;
    event.preventDefault();
    const target =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? stops.length - 1
          : Math.min(stops.length - 1, Math.max(0, index + (event.key === 'ArrowDown' ? 1 : -1)));
    stops[target]?.focus();
  };

  const isCollapsed = (key: string) => !rail && collapsed.includes(key);
  const header = (key: string, label: MessageKey, hasActive: boolean) => {
    const closed = isCollapsed(key);
    return (
      <button
        type="button"
        className="nav-heading sidebar-section-toggle"
        aria-expanded={!closed}
        aria-controls={`${baseId}-${key}`}
        onClick={() => toggle(key)}
      >
        <span className="sidebar-chevron" aria-hidden="true">
          ›
        </span>
        <span className="sidebar-section-label">{t(label)}</span>
        {closed && hasActive ? (
          <>
            <span className="sidebar-active-dot" aria-hidden="true" />
            <span className="visually-hidden">{t('sidebar.containsCurrent')}</span>
          </>
        ) : null}
      </button>
    );
  };

  return (
    <nav ref={navRef} aria-label={t('nav.primary')} onKeyDown={onKeyDown}>
      {pinned.length > 0 || pinError ? (
        <div className="nav-section turaco-rail-section sidebar-section sidebar-pinned">
          {header(PINNED_KEY, 'sidebar.section.pinned', activePin !== undefined)}
          <div
            id={`${baseId}-${PINNED_KEY}`}
            className="sidebar-items"
            role="group"
            aria-label={t('sidebar.section.pinned')}
            hidden={isCollapsed(PINNED_KEY)}
          >
            {pinned.map((item) => {
              const label = countLabel(item.count, item.countCapped);
              return (
                <div className="sidebar-pin" key={item.viewId}>
                  <Link
                    to={item.href}
                    title={rail ? item.name : undefined}
                    aria-label={rail ? item.name : undefined}
                    aria-current={item === activePin ? 'page' : undefined}
                    onContextMenu={(event) => {
                      event.preventDefault();
                      menu.openAtPoint(
                        pinActions(item),
                        { x: event.clientX, y: event.clientY },
                        event.currentTarget,
                        item.name,
                      );
                    }}
                    onKeyDown={(event) => {
                      if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                        event.preventDefault();
                        menu.openAtElement(pinActions(item), event.currentTarget, item.name);
                      }
                    }}
                  >
                    <span className="turaco-rail-icon">
                      <NavIcon id={resourceIcon[item.resource] ?? 'ticketQueue'} />
                    </span>
                    <span className="turaco-rail-label">{item.name}</span>
                    {/* Q-C slot: queue counts (GET /me/sidebar will return `count` and `countCapped`). */}
                    {label !== undefined && !rail ? (
                      <span
                        className="badge badge-info nav-count sidebar-count"
                        data-slot="queue-count"
                        aria-label={t('sidebar.count', { count: label })}
                      >
                        {label}
                      </span>
                    ) : null}
                  </Link>
                  <button
                    type="button"
                    className="sidebar-pin-more"
                    aria-haspopup="menu"
                    aria-label={t('sidebar.pin.actions', { name: item.name })}
                    onClick={(event) =>
                      menu.openAtElement(pinActions(item), event.currentTarget, item.name)
                    }
                  >
                    <span aria-hidden="true">⋯</span>
                  </button>
                </div>
              );
            })}
            {pinError ? (
              <p role="alert" className="sidebar-error">
                {t('sidebar.pin.error')}
              </p>
            ) : null}
          </div>
        </div>
      ) : null}
      {sections.map(({ key, label, items }) => {
        const hasActive = items.some((item) => item.pattern === activePath) && !activePin;
        return (
          <div className="nav-section turaco-rail-section sidebar-section" key={key}>
            {header(key, label, hasActive)}
            <div
              id={`${baseId}-${key}`}
              className="sidebar-items"
              role="group"
              aria-label={t(label)}
              hidden={isCollapsed(key)}
            >
              {items.map((item) => (
                <Link
                  key={item.id}
                  to={item.pattern}
                  title={rail ? t(item.titleKey) : undefined}
                  aria-label={rail ? t(item.titleKey) : undefined}
                  aria-current={item.pattern === activePath && !activePin ? 'page' : undefined}
                >
                  <span className="turaco-rail-icon">
                    <NavIcon id={item.id} />
                  </span>
                  <span className="turaco-rail-label">{t(item.titleKey)}</span>
                  {item.id === 'notifications' && unreadLabel && (
                    <span
                      className="badge badge-info nav-count"
                      aria-label={unreadAria ?? undefined}
                    >
                      {unreadLabel}
                    </span>
                  )}
                </Link>
              ))}
            </div>
          </div>
        );
      })}
      {menu.menu}
    </nav>
  );
}
