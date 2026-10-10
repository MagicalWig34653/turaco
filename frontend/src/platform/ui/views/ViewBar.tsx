import { useEffect, useRef, useState } from 'react';
import type { ApiError } from '../../api/client';
import { asApiError } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import type { MessageKey } from '../../i18n/i18n';
import { useSession } from '../../session/SessionProvider';
import { Alert, Badge } from '../Alert';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { Button } from '../Button';
import { useContextMenu, type MenuItem } from '../ContextMenu';
import { ConfirmDialog } from '../Dialog';
import { listConditions } from '../query/filterModel';
import { useQueryLabels } from '../query/queryLabels';
import type { QueryList } from '../query/useQueryList';
import { notifySidebarChanged, viewsApi, type SavedView, type ViewResource } from './api';
import {
  abilities,
  addPin,
  isDirty,
  isEmptyQuery,
  pinPayload,
  removePin,
  stateToDefinition,
  unavailableConditions,
  unavailableSortCount,
  warningConditionPaths,
} from './model';
import { PinRulesDialog } from './PinRulesDialog';
import { SaveViewDialog } from './SaveViewDialog';
import { ShareDialog } from './ShareDialog';
import { ViewsMenu } from './ViewsMenu';
import './views.css';

type Dialog =
  | { type: 'create' }
  | { type: 'edit' }
  | { type: 'share'; view: SavedView }
  | { type: 'pinRules'; view: SavedView }
  | { type: 'archive'; view: SavedView };

/**
 * Saved-View controls of the filter workbench: the Views dropdown, save / save as, a "modified"
 * indicator, share, pin and the notice for conditions the viewer cannot use.
 */
export function ViewBar<T>({
  query,
  visibleColumns,
  offeredColumns,
}: {
  query: QueryList<T>;
  visibleColumns: string[] | undefined;
  offeredColumns: string[];
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const { view, state, catalog } = query;
  const labels = useQueryLabels(catalog);
  const menu = useContextMenu();
  const opener = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const [notice, setNotice] = useState('');

  const rights = view ? abilities(view) : undefined;
  const viewName = view ? (view.nameKey ? t(view.nameKey as MessageKey) : view.name) : '';
  const dirty = view ? isDirty(view, state, visibleColumns, offeredColumns) : false;

  const unavailable = (() => {
    if (!view) return [];
    const byPath = new Map(
      unavailableConditions(state.filter.root, catalog).map((entry) => [
        entry.path.join('.'),
        entry.node,
      ]),
    );
    const all = listConditions(state.filter.root);
    for (const path of warningConditionPaths(query.warnings)) {
      const found = all.find((entry) => entry.path.join('.') === path.join('.'));
      if (found) byPath.set(path.join('.'), found.node);
    }
    return [...byPath.values()];
  })();
  const sortWarnings = view ? unavailableSortCount(query.warnings) : 0;

  const run = async (action: () => Promise<void>, success?: string) => {
    setBusy(true);
    setError(undefined);
    setNotice('');
    try {
      await action();
      if (success) setNotice(success);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const currentDefinition = () => stateToDefinition(state, visibleColumns);
  const save = () =>
    view &&
    run(async () => {
      query.refreshView(
        await viewsApi.update(view.id, {
          expectedVersion: view.version,
          definition: currentDefinition(),
        }),
      );
    }, t('views.saved'));

  const pin = (target: SavedView, pinned: boolean) =>
    run(
      async () => {
        const current = await viewsApi.pins();
        const next = pinned ? addPin(current.items, target) : removePin(current.items, target.id);
        await viewsApi.replacePins(pinPayload(next));
        if (view?.id === target.id) query.refreshView({ ...view, pinned });
        setReloadToken((value) => value + 1);
        notifySidebarChanged();
      },
      pinned ? t('views.pinnedDone') : t('views.unpinnedDone'),
    );

  const duplicate = (target: SavedView) =>
    run(async () => {
      query.openView(await viewsApi.duplicate(target.id));
      setReloadToken((value) => value + 1);
    }, t('views.duplicated'));

  const takeOver = (target: SavedView) =>
    run(async () => {
      const result = await viewsApi.takeOver(target.id, target.version);
      if (view?.id === target.id) query.refreshView(result);
      setReloadToken((value) => value + 1);
    }, t('views.takenOver'));

  const reloadView = () =>
    view &&
    run(async () => {
      query.refreshView(await viewsApi.get(view.id));
    });

  const open_ = (target: SavedView) => {
    if (target.access === 'admin') return;
    setOpen(false);
    setError(undefined);
    setNotice('');
    query.openView(target);
  };

  /** Actions on any View of the list or the current one. */
  const actionsFor = (target: SavedView): MenuItem[] => {
    const rights = abilities(target);
    const items: MenuItem[] = [
      rights.canRun
        ? { id: 'open', label: t('views.action.open'), onSelect: () => open_(target) }
        : {
            id: 'open',
            label: t('views.action.open'),
            disabledReason: t('views.adminOnly'),
            onSelect: () => undefined,
          },
    ];
    if (rights.canRun)
      items.push({
        id: 'duplicate',
        label: t('views.action.duplicate'),
        onSelect: () => void duplicate(target),
      });
    if (rights.canPin)
      items.push({
        id: 'pin',
        label: target.pinned ? t('views.action.unpin') : t('views.action.pin'),
        onSelect: () => void pin(target, !target.pinned),
      });
    if (rights.canManage)
      items.push({
        id: 'share',
        label: t('views.action.share'),
        onSelect: () => setDialog({ type: 'share', view: target }),
      });
    if (rights.canTakeOver)
      items.push({
        id: 'takeOver',
        label: t('views.action.takeOver'),
        onSelect: () => void takeOver(target),
      });
    if (rights.canManage)
      items.push(
        { id: 'sep', separator: true },
        {
          id: 'archive',
          label: t('views.action.archive'),
          danger: true,
          onSelect: () => setDialog({ type: 'archive', view: target }),
        },
      );
    return items;
  };

  const moreItems = (): MenuItem[] => {
    if (!view || !rights) return [];
    if (view.system)
      return [{ id: 'close', label: t('views.action.close'), onSelect: query.closeView }];
    const items: MenuItem[] = [];
    if (rights.canEdit)
      items.push({
        id: 'rename',
        label: t('views.action.rename'),
        onSelect: () => setDialog({ type: 'edit' }),
      });
    if (can('views.pin_for_groups') && rights.canPin)
      items.push({
        id: 'pinRules',
        label: t('views.action.pinForGroups'),
        onSelect: () => setDialog({ type: 'pinRules', view }),
      });
    items.push(...actionsFor(view).filter((item) => !['open', 'pin', 'share'].includes(item.id)));
    items.push(
      { id: 'sep2', separator: true },
      { id: 'close', label: t('views.action.close'), onSelect: query.closeView },
    );
    return items;
  };

  useEffect(() => {
    // Moving to another View clears the previous outcome message.
    setNotice('');
    setError(undefined);
  }, [view?.id]);

  const emptyQuery = isEmptyQuery(state);

  return (
    <div className="view-bar" role="group" aria-label={t('views.bar')}>
      <div className="view-bar-row">
        <div className="views-menu-wrap">
          <Button
            ref={opener}
            className="views-menu-trigger"
            aria-expanded={open}
            aria-haspopup="true"
            onClick={() => {
              setOpen(!open);
              setReloadToken((value) => value + 1);
            }}
          >
            {t('views.menu')} <span aria-hidden="true">▾</span>
          </Button>
          {open ? (
            <ViewsMenu
              resource={query.resource as ViewResource}
              currentId={view?.id}
              reloadToken={reloadToken}
              onOpen={open_}
              onActions={(target, element, point) => {
                const items = actionsFor(target);
                if (point) menu.openAtPoint(items, point, element, target.name);
                else menu.openAtElement(items, element, target.name);
              }}
              onClose={(restoreFocus) => {
                setOpen(false);
                if (restoreFocus) opener.current?.focus();
              }}
            />
          ) : null}
        </div>

        {view && rights ? (
          <span className="view-current">
            <strong title={view.description || undefined}>{viewName}</strong>
            {view.system ? (
              <Badge tone="info">{t('views.systemBadge')}</Badge>
            ) : rights.readOnly ? (
              <Badge tone="neutral">{t('views.readOnly')}</Badge>
            ) : null}
            {view.visibility === 'shared' ? (
              <Badge tone="info">{t('views.sharedBadge')}</Badge>
            ) : null}
            {dirty ? (
              <span role="status">
                <Badge tone="warning">{t('views.modified')}</Badge>
              </span>
            ) : null}
          </span>
        ) : null}

        {view && rights && dirty && rights.canEdit ? (
          <Button variant="primary" busy={busy} onClick={() => void save()}>
            {t('views.save')}
          </Button>
        ) : null}
        {view && dirty ? (
          <Button disabled={busy} onClick={() => query.openView(view)}>
            {t('views.revert')}
          </Button>
        ) : null}
        <Button
          disabled={busy || (!view && emptyQuery)}
          title={!view && emptyQuery ? t('views.nothingToSave') : undefined}
          onClick={() => setDialog({ type: 'create' })}
        >
          {view ? t('views.saveAs') : t('query.saveView')}
        </Button>
        {view && rights?.canManage ? (
          <Button onClick={() => setDialog({ type: 'share', view })}>{t('views.share')}</Button>
        ) : null}
        {view && rights?.canPin ? (
          <Button
            aria-pressed={view.pinned}
            disabled={busy}
            onClick={() => void pin(view, !view.pinned)}
          >
            {view.pinned ? t('views.unpin') : t('views.pin')}
          </Button>
        ) : null}
        {view ? (
          <Button
            aria-haspopup="menu"
            aria-label={t('views.more')}
            onClick={(event) => menu.openAtElement(moreItems(), event.currentTarget, viewName)}
          >
            <span aria-hidden="true">⋯</span>
          </Button>
        ) : null}
        {notice ? (
          <span role="status" className="view-notice">
            {notice}
          </span>
        ) : null}
      </div>

      {query.viewError ? <ApiErrorAlert error={query.viewError} /> : null}
      {error ? (
        <>
          <ApiErrorAlert error={error} />
          {error.code === 'views.conflict' && view ? (
            <Button disabled={busy} onClick={() => void reloadView()}>
              {t('views.reload')}
            </Button>
          ) : null}
        </>
      ) : null}
      {rights?.readOnly && dirty ? <p className="field-hint">{t('views.readOnlyHint')}</p> : null}
      {view?.visibility === 'shared' && rights?.canEdit && dirty ? (
        <p className="field-hint">{t('views.editSharedHint')}</p>
      ) : null}
      {unavailable.length > 0 || sortWarnings > 0 ? (
        <Alert kind="warning">
          <p>{t('views.unavailable', { count: unavailable.length + sortWarnings })}</p>
          {unavailable.length > 0 ? (
            <ul className="view-unavailable">
              {unavailable.map((node, index) => (
                <li key={`${node.field}.${node.op}.${index}`}>
                  {labels.fieldLabel(node.field)} {labels.opLabel(node.op)}
                </li>
              ))}
            </ul>
          ) : null}
          <p className="field-hint">{t('views.unavailableHint')}</p>
        </Alert>
      ) : null}
      {menu.menu}

      {dialog?.type === 'create' ? (
        <SaveViewDialog
          mode="create"
          initialName={view ? t('views.copyName', { name: viewName }).slice(0, 80) : ''}
          onClose={() => setDialog(null)}
          onSubmit={async (name, description) => {
            const created = await viewsApi.create({
              resource: query.resource as ViewResource,
              name,
              ...(description ? { description } : {}),
              definition: currentDefinition(),
            });
            query.openView(created);
            setReloadToken((value) => value + 1);
            setNotice(t('views.created'));
          }}
        />
      ) : null}
      {dialog?.type === 'edit' && view ? (
        <SaveViewDialog
          mode="edit"
          initialName={view.name}
          initialDescription={view.description}
          onClose={() => setDialog(null)}
          onSubmit={async (name, description) => {
            query.refreshView(
              await viewsApi.update(view.id, { expectedVersion: view.version, name, description }),
            );
            setReloadToken((value) => value + 1);
          }}
        />
      ) : null}
      {dialog?.type === 'share' ? (
        <ShareDialog
          view={dialog.view}
          onClose={() => setDialog(null)}
          onChanged={(updated) => {
            if (view?.id === updated.id) query.refreshView(updated);
            setReloadToken((value) => value + 1);
            setNotice(t('views.shareSaved'));
          }}
        />
      ) : null}
      {dialog?.type === 'pinRules' ? (
        <PinRulesDialog view={dialog.view} onClose={() => setDialog(null)} />
      ) : null}
      {dialog?.type === 'archive' ? (
        <ConfirmDialog
          title={t('views.archive.title', { name: dialog.view.name })}
          message={<p>{t('views.archive.message')}</p>}
          confirmLabel={t('views.action.archive')}
          danger
          busy={busy}
          onCancel={() => setDialog(null)}
          onConfirm={() => {
            const target = dialog.view;
            setDialog(null);
            void run(async () => {
              await viewsApi.archive(target.id, target.version);
              if (view?.id === target.id) query.closeView();
              setReloadToken((value) => value + 1);
              notifySidebarChanged();
            }, t('views.archived'));
          }}
        />
      ) : null}
    </div>
  );
}
