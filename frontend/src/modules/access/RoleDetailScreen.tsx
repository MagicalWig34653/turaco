import { useMemo, useRef, useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useOptionalText } from '../../platform/i18n/optionalText';
import { useModules } from '../../platform/modules/ModulesProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Tabs } from '../../platform/ui/Workspace';
import { diffPermissions, missingNeeds, riskCounts, sameSet, sodConflicts } from './accessModel';
import { accessApi } from './api';
import { PermissionPicker, RiskBadge } from './PermissionPicker';
import type { Permission, Role, RoleTemplate, SodRule } from './types';
import { useActor } from './useActor';

type EditorProps = {
  role: Role;
  permissions: readonly Permission[];
  templates: readonly RoleTemplate[];
  rules: readonly SodRule[];
  canManage: boolean;
  onSaved: () => void;
};

function RoleEditor({ role, permissions, templates, rules, canManage, onSaved }: EditorProps) {
  const { t, locale } = useI18n();
  const text = useOptionalText();
  const { enabled } = useModules();
  const actor = useActor();
  const readOnly = role.builtIn || !canManage;
  const [name, setName] = useState(role.name);
  const [description, setDescription] = useState(role.description);
  const [selected, setSelected] = useState<Set<string>>(new Set(role.permissions));
  const [reviewing, setReviewing] = useState(false);
  const [saved, setSaved] = useState(false);
  const [nameError, setNameError] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteError, setDeleteError] = useState<ApiError | undefined>(undefined);
  const [deleting, setDeleting] = useState(false);
  // A retry after an acknowledgement must not repeat the rename that already went through.
  const progress = useRef<{ version: number | undefined; metaDone: boolean }>({
    version: role.version,
    metaDone: false,
  });

  const byName = useMemo(
    () => new Map(permissions.map((permission) => [permission.name, permission])),
    [permissions],
  );
  const metaChanged = name.trim() !== role.name || description !== role.description;
  const permissionsChanged = !sameSet(selected, role.permissions);
  const dirty = metaChanged || permissionsChanged;
  const diff = diffPermissions(role.permissions, selected);
  const template = role.templateKey
    ? templates.find((entry) => entry.key === role.templateKey)
    : undefined;
  const drift = template?.roles.find((use) => use.roleId === role.id);

  const save = async (extras: { acknowledgedRules?: string[]; reason?: string }) => {
    const state = progress.current;
    if (metaChanged && !state.metaDone) {
      const updated = await accessApi.updateRole(role.id, {
        ...(state.version !== undefined ? { expectedVersion: state.version } : {}),
        name: name.trim(),
        description,
      });
      state.version = updated.version;
      state.metaDone = true;
    }
    if (permissionsChanged) {
      await accessApi.setRolePermissions(role.id, {
        ...(state.version !== undefined ? { expectedVersion: state.version } : {}),
        permissions: [...selected].sort(),
        ...extras,
      });
    }
  };

  const remove = async () => {
    setDeleting(true);
    setDeleteError(undefined);
    try {
      await accessApi.deleteRole(role.id, role.version);
      navigate('/admin/roles');
    } catch (cause) {
      setDeleteError(asApiError(cause));
      setDeleting(false);
    }
  };

  return (
    <>
      {role.builtIn ? <Alert kind="info">{t('roles.builtIn.notice')}</Alert> : null}
      {saved && !dirty ? <Alert kind="success">{t('roles.saved')}</Alert> : null}
      {template && drift && drift.missing.length > 0 ? (
        <Alert kind="warning">
          <p>
            <strong>
              {t('roles.drift.title', {
                template: text(template.nameKey, template.name),
                from: drift.templateVersion,
                to: template.version,
              })}
            </strong>
          </p>
          <p>{t('roles.drift.body', { count: drift.missing.length })}</p>
          <ul className="chips">
            {drift.missing.map((permission) => (
              <li key={permission}>
                <code>{permission}</code>
              </li>
            ))}
          </ul>
          {!readOnly ? (
            <Button
              onClick={() =>
                setSelected(new Set([...selected, ...drift.missing.filter((p) => byName.has(p))]))
              }
            >
              {t('roles.drift.add')}
            </Button>
          ) : null}
        </Alert>
      ) : null}
      <form
        className="form adm-wide-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (name.trim() === '') {
            setNameError(true);
            return;
          }
          setNameError(false);
          progress.current = { version: role.version, metaDone: false };
          setReviewing(true);
        }}
        noValidate
      >
        <dl className="facts">
          <dt>{t('roles.field.key')}</dt>
          <dd>
            <code>{role.key}</code> {role.builtIn ? <Badge>{t('roles.builtIn')}</Badge> : null}
            {role.templateKey ? (
              <Badge tone="info">{t('roles.fromTemplate', { key: role.templateKey })}</Badge>
            ) : null}
          </dd>
          <dt>{t('roles.col.assignments')}</dt>
          <dd>
            {role.activeAssignments}{' '}
            <Link to={`/admin/role-assignments?roleId=${encodeURIComponent(role.id)}`}>
              {t('roles.viewAssignments')}
            </Link>
          </dd>
          <dt>{t('roles.field.updated')}</dt>
          <dd>{formatDateTime(locale, role.updatedAt)}</dd>
        </dl>
        <TextField
          label={t('roles.field.name')}
          error={nameError ? t('roles.name.required') : undefined}
          value={name}
          onChange={(event) => setName(event.target.value)}
          readOnly={readOnly}
          maxLength={200}
          required
        />
        <TextArea
          label={t('roles.field.description')}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          readOnly={readOnly}
          maxLength={2000}
          rows={3}
        />
        <h2>{t('roles.field.permissions')}</h2>
        <PermissionPicker
          permissions={permissions}
          selected={selected}
          onChange={(next) => {
            setSelected(next);
            setSaved(false);
          }}
          disabled={readOnly}
          initialOnlySelected={readOnly}
          actor={actor}
          moduleEnabled={enabled}
        />
        {!readOnly ? (
          <div className="form-actions">
            <Button type="submit" variant="primary" disabled={!dirty}>
              {t('roles.save.review')}
            </Button>
            <Button
              variant="danger"
              onClick={() => setConfirmDelete(true)}
              disabled={role.activeAssignments > 0}
              aria-describedby={role.activeAssignments > 0 ? 'delete-hint' : undefined}
            >
              {t('roles.delete.action')}
            </Button>
            {role.activeAssignments > 0 ? (
              <p className="field-hint" id="delete-hint">
                {t('roles.delete.blocked', { count: role.activeAssignments })}
              </p>
            ) : null}
          </div>
        ) : null}
      </form>
      {reviewing ? (
        <SaveReview
          role={role}
          metaChanged={metaChanged}
          diff={diff}
          selected={selected}
          byName={byName}
          rules={rules}
          run={save}
          onDone={() => {
            setReviewing(false);
            setSaved(true);
            onSaved();
          }}
          onClose={() => setReviewing(false)}
        />
      ) : null}
      {confirmDelete ? (
        <ConfirmDialog
          title={t('roles.delete.title')}
          message={<p>{t('roles.delete.message', { name: role.name })}</p>}
          confirmLabel={t('roles.delete.action')}
          danger
          busy={deleting}
          error={deleteError ? t(errorMessageKey(deleteError)) : undefined}
          onConfirm={() => void remove()}
          onCancel={() => setConfirmDelete(false)}
        />
      ) : null}
    </>
  );
}

function SaveReview({
  role,
  metaChanged,
  diff,
  selected,
  byName,
  rules,
  run,
  onDone,
  onClose,
}: {
  role: Role;
  metaChanged: boolean;
  diff: { added: string[]; removed: string[] };
  selected: ReadonlySet<string>;
  byName: ReadonlyMap<string, Permission>;
  rules: readonly SodRule[];
  run: (extras: { acknowledgedRules?: string[]; reason?: string }) => Promise<void>;
  onDone: () => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const text = useOptionalText();
  const members = useAsync((signal) => accessApi.roleMembers(role.id, signal), [role.id]);
  const missing = missingNeeds(selected, byName);
  const conflicts = sodConflicts(selected, rules);
  const counts = riskCounts(diff.added, byName);
  const people = members.data ? members.data.items.length : undefined;
  const list = (names: string[]) => (
    <ul className="adm-diff-list">
      {names.map((name) => (
        <li key={name}>
          <code>{name}</code>{' '}
          {byName.get(name) ? <RiskBadge risk={(byName.get(name) as Permission).risk} /> : null}
        </li>
      ))}
    </ul>
  );
  return (
    <GuardedActionDialog
      title={t('roles.save.title', { name: role.name })}
      confirmLabel={t('action.save')}
      run={(extras) =>
        run({
          ...(extras.acknowledgedRules ? { acknowledgedRules: extras.acknowledgedRules } : {}),
          ...(extras.reason ? { reason: extras.reason } : {}),
        })
      }
      onDone={onDone}
      onClose={onClose}
    >
      {metaChanged ? <p>{t('roles.save.metaChanged')}</p> : null}
      <p>
        <strong>{t('roles.save.impact')}</strong>{' '}
        {role.activeAssignments === 0
          ? t('roles.save.impact.none')
          : people !== undefined
            ? t('roles.save.impact.some', {
                assignments: role.activeAssignments,
                people: members.data?.capped ? `${people}+` : people,
              })
            : t('roles.save.impact.assignments', { assignments: role.activeAssignments })}
      </p>
      {diff.added.length > 0 ? (
        <section aria-label={t('roles.save.added')}>
          <h3>{t('roles.save.added')}</h3>
          {counts.high > 0 ? (
            <Alert kind="warning">{t('roles.save.addedHigh', { count: counts.high })}</Alert>
          ) : null}
          {list(diff.added)}
        </section>
      ) : null}
      {diff.removed.length > 0 ? (
        <section aria-label={t('roles.save.removed')}>
          <h3>{t('roles.save.removed')}</h3>
          {role.activeAssignments > 0 ? (
            <p className="field-hint">{t('roles.save.removedHint')}</p>
          ) : null}
          {list(diff.removed)}
        </section>
      ) : null}
      {diff.added.length === 0 && diff.removed.length === 0 ? (
        <p>{t('roles.save.noPermissionChange')}</p>
      ) : null}
      {missing.length > 0 ? (
        <Alert kind="warning">
          <p>{t('roles.save.missingNeeds')}</p>
          <ul>
            {missing.map((entry) => (
              <li key={entry.permission}>
                {t('picker.needs.line', {
                  permission: entry.permission,
                  needs: entry.needs.join(', '),
                })}
              </li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {conflicts.length > 0 ? (
        <Alert kind="warning">
          <p>{t('roles.save.sodPreview')}</p>
          <ul>
            {conflicts.map((rule) => (
              <li key={rule.key}>{text(rule.messageKey, rule.key)}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
    </GuardedActionDialog>
  );
}

function HoldersTab({ roleId }: { roleId: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const members = useAsync((signal) => accessApi.roleMembers(roleId, signal), [roleId]);
  if (members.loading) return <p role="status">{t('state.loading')}</p>;
  if (members.error) return <ApiErrorAlert error={members.error} onRetry={members.reload} />;
  const items = members.data?.items ?? [];
  return (
    <section aria-label={t('roles.holders.title')}>
      <p className="subtitle">{t('roles.holders.intro')}</p>
      {members.data?.capped ? (
        <Alert kind="info">{t('roles.holders.capped', { limit: members.data.limit })}</Alert>
      ) : null}
      {items.length === 0 ? (
        <p className="empty">{t('roles.holders.empty')}</p>
      ) : (
        <ul className="adm-holder-list">
          {items.map((member) => (
            <li key={`${member.userId}:${member.source}`}>
              {can('organization.view') ? (
                <Link to={`/admin/users/${encodeURIComponent(member.userId)}`}>
                  {member.displayName}
                </Link>
              ) : (
                member.displayName
              )}{' '}
              <Badge tone={member.source === 'direct' ? 'neutral' : 'info'}>
                {t(member.source === 'direct' ? 'roles.holders.direct' : 'roles.holders.group')}
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function RoleDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const [tab, setTab] = useState<'permissions' | 'holders'>('permissions');
  const role = useAsync((signal) => accessApi.role(id, signal), [id]);
  const permissions = useAsync((signal) => accessApi.permissions(signal), []);
  const templates = useAsync(
    (signal) => accessApi.templates(signal).catch(() => ({ items: [] as RoleTemplate[] })),
    [],
  );
  const rules = useAsync(
    (signal) => accessApi.sodRules(signal).catch(() => ({ items: [] as SodRule[] })),
    [],
  );

  return (
    <>
      <PageHeader
        title={role.data?.name ?? t('roles.detail.title')}
        eyebrow={t('nav.roles')}
        intro={role.data?.description ? role.data.description : undefined}
        actions={
          <>
            {can('platform.roles.manage') && role.data ? (
              <Link
                to={`/admin/roles/new?copy=${encodeURIComponent(role.data.id)}`}
                className="btn btn-secondary"
              >
                {t('roles.copy.action')}
              </Link>
            ) : null}
            <Link to="/admin/roles" className="btn btn-secondary">
              {t('roles.back')}
            </Link>
          </>
        }
      />
      {role.loading || permissions.loading ? <p role="status">{t('state.loading')}</p> : null}
      {role.error ? <ApiErrorAlert error={role.error} onRetry={role.reload} /> : null}
      {permissions.error ? (
        <ApiErrorAlert error={permissions.error} onRetry={permissions.reload} />
      ) : null}
      {role.data && permissions.data ? (
        <>
          <Tabs
            idPrefix="role"
            active={tab}
            onChange={(next) => setTab(next as 'permissions' | 'holders')}
            items={[
              { id: 'permissions', label: t('roles.tab.permissions') },
              { id: 'holders', label: t('roles.tab.holders') },
            ]}
          />
          {tab === 'permissions' ? (
            <div
              role="tabpanel"
              id="role-panel-permissions"
              aria-labelledby="role-tab-permissions"
              className="adm-panel"
            >
              <RoleEditor
                key={role.data.id}
                role={role.data}
                permissions={permissions.data.items}
                templates={templates.data?.items ?? []}
                rules={rules.data?.items ?? []}
                canManage={can('platform.roles.manage')}
                onSaved={role.reload}
              />
            </div>
          ) : (
            <div
              role="tabpanel"
              id="role-panel-holders"
              aria-labelledby="role-tab-holders"
              className="adm-panel"
            >
              <HoldersTab roleId={role.data.id} />
            </div>
          )}
        </>
      ) : null}
    </>
  );
}
