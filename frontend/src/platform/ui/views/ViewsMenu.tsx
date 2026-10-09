import { useEffect, useId, useRef, useState } from 'react';
import { useAsync } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import type { MessageKey } from '../../i18n/i18n';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { useDebouncedValue } from '../hooks';
import { viewsApi, type SavedView, type ViewResource } from './api';
import { filterViews, groupViews, type ViewSection } from './model';

const sections: ViewSection[] = ['mine', 'shared', 'global'];

/**
 * Dropdown listing the caller's own, shared and global Views of one resource, with search.
 * Rows open the View; a context menu (right click, Menu key or the row button) offers the rest.
 */
export function ViewsMenu({
  resource,
  currentId,
  reloadToken,
  onOpen,
  onActions,
  onClose,
}: {
  resource: ViewResource;
  currentId: string | undefined;
  reloadToken: number;
  onOpen: (view: SavedView) => void;
  onActions: (view: SavedView, opener: HTMLElement, point?: { x: number; y: number }) => void;
  onClose: (restoreFocus: boolean) => void;
}) {
  const { t } = useI18n();
  const root = useRef<HTMLDivElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const searchId = useId();
  const [search, setSearch] = useState('');
  const debounced = useDebouncedValue(search, 150);
  const list = useAsync(
    (signal) => viewsApi.list({ resource, scope: 'all' }, signal),
    [resource, reloadToken],
  );
  const shown = groupViews(filterViews(list.data?.items ?? [], debounced));
  const total = sections.reduce((sum, key) => sum + shown[key].length, 0);

  useEffect(() => {
    root.current?.querySelector<HTMLElement>('input')?.focus();
    const outside = (event: PointerEvent) => {
      const target = event.target as Element | null;
      if (target?.closest('.context-menu, dialog')) return;
      if (!root.current?.contains(target) && !target?.closest('.views-menu-trigger'))
        closeRef.current(false);
    };
    document.addEventListener('pointerdown', outside, true);
    return () => document.removeEventListener('pointerdown', outside, true);
  }, []);

  return (
    <div
      ref={root}
      className="views-menu-panel"
      role="group"
      aria-label={t('views.menu')}
      onKeyDown={(event) => {
        if (event.key === 'Escape') {
          event.stopPropagation();
          onClose(true);
        } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          const stops = [
            ...(root.current?.querySelectorAll<HTMLElement>('input, [data-view-item]') ?? []),
          ];
          const index = stops.indexOf(document.activeElement as HTMLElement);
          const next =
            stops[(index + (event.key === 'ArrowDown' ? 1 : -1) + stops.length) % stops.length];
          if (next) {
            event.preventDefault();
            next.focus();
          }
        }
      }}
    >
      <label htmlFor={searchId} className="visually-hidden">
        {t('views.search')}
      </label>
      <input
        id={searchId}
        type="search"
        value={search}
        maxLength={80}
        autoComplete="off"
        placeholder={t('views.search')}
        onChange={(event) => setSearch(event.target.value)}
      />
      {list.error ? <ApiErrorAlert error={list.error} onRetry={list.reload} /> : null}
      {list.loading && !list.data ? <p role="status">{t('state.loading')}</p> : null}
      {list.data && total === 0 ? (
        <p className="empty">{t(search.trim() ? 'views.noMatch' : 'views.none')}</p>
      ) : null}
      {sections.map((key) =>
        shown[key].length === 0 ? null : (
          <section key={key} aria-label={t(`views.section.${key}` as MessageKey)}>
            <h3>{t(`views.section.${key}` as MessageKey)}</h3>
            <ul>
              {shown[key].map((view) => (
                <li key={view.id} className="views-menu-item">
                  <button
                    type="button"
                    data-view-item
                    className="views-menu-open"
                    aria-current={view.id === currentId ? 'true' : undefined}
                    onClick={() => onOpen(view)}
                    onContextMenu={(event) => {
                      event.preventDefault();
                      onActions(view, event.currentTarget, { x: event.clientX, y: event.clientY });
                    }}
                    onKeyDown={(event) => {
                      if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                        event.preventDefault();
                        onActions(view, event.currentTarget);
                      }
                    }}
                  >
                    <span className="views-menu-name">{view.name}</span>
                    <small>
                      {[
                        view.access === 'owner' ? undefined : view.ownerName,
                        view.access === 'use' ? t('views.readOnly') : undefined,
                        view.pinned ? t('views.pinned') : undefined,
                      ]
                        .filter(Boolean)
                        .join(' · ')}
                    </small>
                  </button>
                  <button
                    type="button"
                    data-view-item
                    className="views-menu-more"
                    aria-haspopup="menu"
                    aria-label={t('views.actionsFor', { name: view.name })}
                    onClick={(event) => onActions(view, event.currentTarget)}
                  >
                    <span aria-hidden="true">⋯</span>
                  </button>
                </li>
              ))}
            </ul>
          </section>
        ),
      )}
    </div>
  );
}
