import { useEffect, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { useContextMenu, type MenuItem } from '../../platform/ui/ContextMenu';
import { Checkbox, Select, TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { peopleAdminApi } from './adminApi';
import {
  LOCATION_MAX_DEPTH,
  buildTree,
  findNode,
  flattenTree,
  moveTargets,
  pathLabel,
  type TreeNode,
} from './adminModel';
import type { OrgNode } from './adminTypes';

export type TreeKind = 'locations' | 'departments';

type Dialog =
  | { type: 'create'; parent: TreeNode<OrgNode> | null }
  | { type: 'edit'; node: TreeNode<OrgNode> }
  | { type: 'move'; node: TreeNode<OrgNode> }
  | { type: 'archive'; node: TreeNode<OrgNode> }
  | { type: 'restore'; node: TreeNode<OrgNode> };

const copy = {
  locations: {
    title: 'locations.title',
    intro: 'locations.intro',
    child: 'locations.addChild',
    root: 'locations.addRoot',
  },
  departments: {
    title: 'departments.title',
    intro: 'departments.intro',
    child: 'departments.addChild',
    root: 'departments.addRoot',
  },
} as const satisfies Record<TreeKind, Record<string, MessageKey>>;

/** Tree of Locations (sites and areas) or Departments with create, rename, move and archive. */
export function OrgTreeScreen({ kind }: { kind: TreeKind }) {
  const { t } = useI18n();
  const { can } = useSession();
  const canManage = can(
    kind === 'locations' ? 'organization.locations.manage' : 'organization.departments.manage',
  );
  const menu = useContextMenu();
  const data = useAsync(
    (signal): Promise<OrgNode[]> =>
      kind === 'locations' ? peopleAdminApi.locations(signal) : peopleAdminApi.departments(signal),
    [kind],
  );
  const [includeInactive, setIncludeInactive] = useState(false);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [selectedId, setSelectedId] = useState<string>('');
  const [focusId, setFocusId] = useState<string>('');
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const seeded = useRef(false);
  const treeRef = useRef<HTMLUListElement>(null);

  const items = data.data ?? [];
  const forest = useMemo(() => buildTree(items, includeInactive), [items, includeInactive]);
  const visible = useMemo(() => flattenTree(forest, expanded), [forest, expanded]);
  const selected = selectedId ? findNode(forest, selectedId) : undefined;
  const maxDepth = kind === 'locations' ? LOCATION_MAX_DEPTH : undefined;
  const archived = items.filter((item) => !item.active).length;

  // Open the first two levels once the data arrives.
  useEffect(() => {
    if (seeded.current || items.length === 0) return;
    seeded.current = true;
    const open = new Set<string>();
    const visit = (nodes: TreeNode<OrgNode>[]) =>
      nodes.forEach((node) => {
        if (node.depth < 2) open.add(node.item.id);
        visit(node.children);
      });
    visit(buildTree(items, false));
    setExpanded(open);
  }, [items]);

  const toggle = (id: string, open?: boolean) =>
    setExpanded((current) => {
      const next = new Set(current);
      if (open ?? !next.has(id)) next.add(id);
      else next.delete(id);
      return next;
    });

  const actionsFor = (node: TreeNode<OrgNode>): MenuItem[] => {
    if (!canManage) return [];
    const isSite = node.item.kind === 'site';
    const tooDeep = maxDepth !== undefined && node.depth >= maxDepth;
    const out: MenuItem[] = [];
    if (node.item.active) {
      out.push(
        tooDeep
          ? {
              id: 'add',
              label: t(copy[kind].child),
              disabledReason: t('locations.maxDepth', { count: LOCATION_MAX_DEPTH }),
              onSelect: () => undefined,
            }
          : {
              id: 'add',
              label: t(copy[kind].child),
              onSelect: () => setDialog({ type: 'create', parent: node }),
            },
        {
          id: 'edit',
          label: t('tree.action.edit'),
          onSelect: () => setDialog({ type: 'edit', node }),
        },
        isSite
          ? {
              id: 'move',
              label: t('tree.action.move'),
              disabledReason: t('locations.siteNoMove'),
              onSelect: () => undefined,
            }
          : {
              id: 'move',
              label: t('tree.action.move'),
              onSelect: () => setDialog({ type: 'move', node }),
            },
        { id: 'sep', separator: true },
        {
          id: 'archive',
          label: t('tree.action.archive'),
          danger: true,
          onSelect: () => setDialog({ type: 'archive', node }),
        },
      );
    } else {
      out.push({
        id: 'restore',
        label: t('tree.action.restore'),
        onSelect: () => setDialog({ type: 'restore', node }),
      });
    }
    return out;
  };

  const focusItem = (id: string) => {
    setFocusId(id);
    requestAnimationFrame(() =>
      treeRef.current?.querySelector<HTMLElement>(`[data-node="${CSS.escape(id)}"]`)?.focus(),
    );
  };

  const onKeyDown = (event: KeyboardEvent<HTMLElement>, node: TreeNode<OrgNode>, index: number) => {
    const open = expanded.has(node.item.id);
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        if (visible[index + 1]) focusItem(visible[index + 1]!.item.id);
        break;
      case 'ArrowUp':
        event.preventDefault();
        if (visible[index - 1]) focusItem(visible[index - 1]!.item.id);
        break;
      case 'Home':
        event.preventDefault();
        if (visible[0]) focusItem(visible[0].item.id);
        break;
      case 'End':
        event.preventDefault();
        if (visible.length) focusItem(visible[visible.length - 1]!.item.id);
        break;
      case 'ArrowRight':
        event.preventDefault();
        if (node.children.length > 0) {
          if (!open) toggle(node.item.id, true);
          else focusItem(node.children[0]!.item.id);
        }
        break;
      case 'ArrowLeft': {
        event.preventDefault();
        if (open && node.children.length > 0) toggle(node.item.id, false);
        else {
          const parentId = node.item.parentId;
          if (parentId && visible.some((entry) => entry.item.id === parentId)) focusItem(parentId);
        }
        break;
      }
      case 'Enter':
      case ' ':
        event.preventDefault();
        setSelectedId(node.item.id);
        break;
      case 'ContextMenu':
        event.preventDefault();
        menu.openAtElement(actionsFor(node), event.currentTarget, node.item.name);
        break;
      case 'F10':
        if (event.shiftKey) {
          event.preventDefault();
          menu.openAtElement(actionsFor(node), event.currentTarget, node.item.name);
        }
        break;
      default:
    }
  };

  const reloadAfter = (openParent?: string) => {
    setDialog(null);
    if (openParent) toggle(openParent, true);
    data.reload();
  };

  return (
    <>
      <PageHeader
        title={t(copy[kind].title)}
        eyebrow={t('sidebar.section.admin')}
        intro={t(copy[kind].intro)}
        actions={
          canManage ? (
            <Button variant="primary" onClick={() => setDialog({ type: 'create', parent: null })}>
              {t(copy[kind].root)}
            </Button>
          ) : null
        }
      />
      {data.error ? <ApiErrorAlert error={data.error} onRetry={data.reload} /> : null}
      <div className="adm-tree-bar">
        <Checkbox
          label={t('tree.showArchived', { count: archived })}
          checked={includeInactive}
          onChange={(event) => setIncludeInactive(event.target.checked)}
        />
        <p className="field-hint" role="status">
          {t('tree.summary', { total: items.filter((item) => item.active).length, archived })}
        </p>
      </div>
      <div className="adm-tree-layout">
        <div>
          {data.loading && !data.data ? <p role="status">{t('state.loading')}</p> : null}
          {!data.loading && forest.length === 0 && !data.error ? (
            <p className="empty">{t(`${kind}.empty`)}</p>
          ) : null}
          {forest.length > 0 ? (
            <ul role="tree" aria-label={t(copy[kind].title)} className="adm-tree" ref={treeRef}>
              {visible.map((node, index) => {
                const id = node.item.id;
                const hasChildren = node.children.length > 0;
                const isOpen = expanded.has(id);
                const roving = focusId ? focusId === id : index === 0;
                return (
                  <li
                    key={id}
                    role="treeitem"
                    aria-level={node.depth}
                    aria-expanded={hasChildren ? isOpen : undefined}
                    aria-selected={selectedId === id}
                    aria-setsize={undefined}
                    data-node={id}
                    tabIndex={roving ? 0 : -1}
                    className={`adm-tree-row${selectedId === id ? ' is-selected' : ''}${node.item.active ? '' : ' is-archived'}`}
                    style={{ paddingInlineStart: `${(node.depth - 1) * 20 + 8}px` }}
                    onKeyDown={(event) => {
                      if (event.target === event.currentTarget) onKeyDown(event, node, index);
                    }}
                    onFocus={(event) => {
                      if (event.target === event.currentTarget) setFocusId(id);
                    }}
                    onClick={() => setSelectedId(id)}
                    onContextMenu={(event) => {
                      const items = actionsFor(node);
                      if (items.length === 0) return;
                      event.preventDefault();
                      setSelectedId(id);
                      menu.openAtPoint(
                        items,
                        { x: event.clientX, y: event.clientY },
                        event.currentTarget,
                        node.item.name,
                      );
                    }}
                  >
                    <span
                      className="adm-tree-toggle"
                      aria-hidden="true"
                      onClick={(event) => {
                        event.stopPropagation();
                        if (hasChildren) toggle(id);
                      }}
                    >
                      {hasChildren ? (isOpen ? '▾' : '▸') : '·'}
                    </span>
                    <span className="adm-tree-name">{node.item.name}</span>
                    {node.item.code ? <span className="adm-chip">{node.item.code}</span> : null}
                    {node.item.kind ? (
                      <span className="adm-sub">{t(`locations.kind.${node.item.kind}`)}</span>
                    ) : null}
                    {hasChildren ? (
                      <span className="adm-sub">
                        {t('tree.children', { count: node.descendants })}
                      </span>
                    ) : null}
                    {!node.item.active ? <Badge tone="warning">{t('tree.archived')}</Badge> : null}
                    {canManage ? (
                      <button
                        type="button"
                        className="table-actions-trigger adm-tree-menu"
                        tabIndex={-1}
                        aria-label={t('tree.rowActions', { name: node.item.name })}
                        aria-haspopup="menu"
                        onClick={(event) => {
                          event.stopPropagation();
                          menu.openAtElement(actionsFor(node), event.currentTarget, node.item.name);
                        }}
                      >
                        ⋯
                      </button>
                    ) : null}
                  </li>
                );
              })}
            </ul>
          ) : null}
        </div>
        <aside className="adm-card adm-tree-detail" aria-label={t('tree.detail.title')}>
          {selected ? (
            <>
              <h2>{selected.item.name}</h2>
              <dl className="adm-rows">
                <div className="adm-row">
                  <dt>{t('tree.detail.path')}</dt>
                  <dd>{pathLabel(items, selected.item.id)}</dd>
                </div>
                {selected.item.kind ? (
                  <div className="adm-row">
                    <dt>{t('tree.detail.kind')}</dt>
                    <dd>{t(`locations.kind.${selected.item.kind}`)}</dd>
                  </div>
                ) : null}
                <div className="adm-row">
                  <dt>{t('tree.detail.code')}</dt>
                  <dd>{selected.item.code || '–'}</dd>
                </div>
                {kind === 'locations' ? (
                  <div className="adm-row">
                    <dt>{t('tree.detail.description')}</dt>
                    <dd>{selected.item.description || '–'}</dd>
                  </div>
                ) : null}
                <div className="adm-row">
                  <dt>{t('tree.detail.children')}</dt>
                  <dd>{selected.children.length}</dd>
                </div>
                <div className="adm-row">
                  <dt>{t('people.col.status')}</dt>
                  <dd>
                    <Badge tone={selected.item.active ? 'success' : 'warning'}>
                      {t(selected.item.active ? 'teams.status.active' : 'tree.archived')}
                    </Badge>
                  </dd>
                </div>
              </dl>
              {canManage ? (
                <div className="adm-actions">
                  {actionsFor(selected)
                    .filter(
                      (item): item is Extract<MenuItem, { onSelect: () => void }> =>
                        !('separator' in item),
                    )
                    .map((item) => (
                      <Button
                        key={item.id}
                        variant={item.danger ? 'danger' : 'secondary'}
                        disabled={item.disabledReason !== undefined}
                        title={item.disabledReason}
                        onClick={item.onSelect}
                      >
                        {item.label}
                      </Button>
                    ))}
                </div>
              ) : null}
            </>
          ) : (
            <p className="empty">{t('tree.detail.empty')}</p>
          )}
          <p className="field-hint">{t('tree.keyboardHint')}</p>
        </aside>
      </div>
      {menu.menu}
      {dialog?.type === 'create' ? (
        <NodeFormDialog
          kind={kind}
          parent={dialog.parent}
          onClose={() => setDialog(null)}
          onDone={() => reloadAfter(dialog.parent?.item.id)}
        />
      ) : null}
      {dialog?.type === 'edit' ? (
        <NodeFormDialog
          kind={kind}
          node={dialog.node}
          parent={null}
          onClose={() => setDialog(null)}
          onDone={() => reloadAfter()}
        />
      ) : null}
      {dialog?.type === 'move' ? (
        <MoveDialog
          kind={kind}
          forest={forest}
          node={dialog.node}
          maxDepth={maxDepth}
          onClose={() => setDialog(null)}
          onDone={() => reloadAfter()}
        />
      ) : null}
      {dialog?.type === 'archive' || dialog?.type === 'restore' ? (
        <GuardedActionDialog
          title={t(dialog.type === 'archive' ? 'tree.archive.title' : 'tree.restore.title', {
            name: dialog.node.item.name,
          })}
          confirmLabel={t(
            dialog.type === 'archive' ? 'tree.action.archive' : 'tree.action.restore',
          )}
          danger={dialog.type === 'archive'}
          run={async (extras) => {
            const node = dialog.node.item;
            const active = dialog.type === 'restore';
            if (kind === 'locations')
              await peopleAdminApi.setLocationActive(
                node.id,
                active,
                node.version,
                extras.confirmImpact === true,
              );
            else
              await peopleAdminApi.setDepartmentActive(
                node.id,
                active,
                node.version,
                extras.confirmImpact === true,
              );
          }}
          onDone={() => reloadAfter()}
          onClose={() => setDialog(null)}
        >
          <p>
            {t(dialog.type === 'archive' ? 'tree.archive.intro' : 'tree.restore.intro', {
              name: dialog.node.item.name,
            })}
          </p>
          {dialog.type === 'archive' && dialog.node.children.length > 0 ? (
            <Alert kind="warning">
              {t('tree.archive.children', { count: dialog.node.children.length })}
            </Alert>
          ) : null}
        </GuardedActionDialog>
      ) : null}
    </>
  );
}

function NodeFormDialog({
  kind,
  node,
  parent,
  onClose,
  onDone,
}: {
  kind: TreeKind;
  node?: TreeNode<OrgNode>;
  parent: TreeNode<OrgNode> | null;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const editing = node !== undefined;
  const [name, setName] = useState(node?.item.name ?? '');
  const [code, setCode] = useState(node?.item.code ?? '');
  const [description, setDescription] = useState(node?.item.description ?? '');
  const changed =
    !editing ||
    name.trim() !== node.item.name ||
    code.trim() !== (node.item.code ?? '') ||
    description.trim() !== (node.item.description ?? '');
  return (
    <GuardedActionDialog
      title={
        editing
          ? t('tree.edit.title', { name: node.item.name })
          : parent
            ? t(
                kind === 'locations'
                  ? 'locations.create.titleChild'
                  : 'departments.create.titleChild',
                { parent: parent.item.name },
              )
            : t(
                kind === 'locations'
                  ? 'locations.create.titleRoot'
                  : 'departments.create.titleRoot',
              )
      }
      confirmLabel={editing ? t('action.save') : t('tree.create.submit')}
      disabled={name.trim() === '' || !changed}
      run={async () => {
        if (editing) {
          const patch = {
            expectedVersion: node.item.version,
            name: name.trim(),
            code: code.trim() || null,
          };
          if (kind === 'locations')
            await peopleAdminApi.updateLocation(node.item.id, {
              ...patch,
              description: description.trim(),
            });
          else await peopleAdminApi.updateDepartment(node.item.id, patch);
        } else if (kind === 'locations') {
          await peopleAdminApi.createLocation({
            kind: parent ? 'area' : 'site',
            parentId: parent?.item.id ?? null,
            name: name.trim(),
            code: code.trim() || null,
            description: description.trim(),
          });
        } else {
          await peopleAdminApi.createDepartment({
            name: name.trim(),
            code: code.trim() || null,
            parentId: parent?.item.id ?? null,
          });
        }
      }}
      onDone={onDone}
      onClose={onClose}
    >
      {kind === 'locations' && !editing ? (
        <p className="field-hint">
          {t(parent ? 'locations.create.areaHint' : 'locations.create.siteHint')}
        </p>
      ) : null}
      <TextField
        label={t('tree.field.name')}
        value={name}
        maxLength={200}
        required
        autoFocus
        onChange={(event) => setName(event.target.value)}
      />
      <TextField
        label={t('tree.field.code')}
        hint={t('tree.field.codeHint')}
        value={code}
        maxLength={40}
        onChange={(event) => setCode(event.target.value)}
      />
      {kind === 'locations' ? (
        <TextArea
          label={t('tree.field.description')}
          value={description}
          maxLength={500}
          rows={3}
          onChange={(event) => setDescription(event.target.value)}
        />
      ) : null}
    </GuardedActionDialog>
  );
}

function MoveDialog({
  kind,
  forest,
  node,
  maxDepth,
  onClose,
  onDone,
}: {
  kind: TreeKind;
  forest: TreeNode<OrgNode>[];
  node: TreeNode<OrgNode>;
  maxDepth: number | undefined;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const targets = moveTargets(forest, node, {
    ...(maxDepth !== undefined ? { maxDepth } : {}),
    allowRoot: kind === 'departments',
  });
  const KEEP = '__keep';
  const ROOT = '__root';
  const [target, setTarget] = useState(KEEP);
  const parentFor = (value: string) => (value === ROOT ? null : value);
  return (
    <GuardedActionDialog
      title={t('tree.move.title', { name: node.item.name })}
      confirmLabel={t('tree.action.move')}
      disabled={target === KEEP}
      run={async () => {
        if (kind === 'locations')
          await peopleAdminApi.moveLocation(node.item.id, node.item.version, parentFor(target));
        else
          await peopleAdminApi.moveDepartment(node.item.id, node.item.version, parentFor(target));
      }}
      onDone={onDone}
      onClose={onClose}
    >
      <p>{t('tree.move.intro')}</p>
      {targets.length === 0 ? <Alert kind="warning">{t('tree.move.none')}</Alert> : null}
      <Select
        label={t('tree.move.target')}
        value={target}
        onChange={(event) => setTarget(event.target.value)}
        options={[
          { value: KEEP, label: t('tree.move.choose') },
          ...(kind === 'departments' && node.item.parentId
            ? [{ value: ROOT, label: t('tree.move.root') }]
            : []),
          ...targets
            .filter((entry) => entry.node !== null && entry.node.item.id !== node.item.parentId)
            .map((entry) => ({ value: entry.node!.item.id, label: entry.label })),
        ]}
      />
      {kind === 'locations' ? (
        <p className="field-hint">{t('locations.move.depthHint', { count: LOCATION_MAX_DEPTH })}</p>
      ) : null}
    </GuardedActionDialog>
  );
}
