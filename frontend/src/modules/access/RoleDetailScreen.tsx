import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { PermissionChecklist } from './PermissionChecklist';
import { accessApi } from './api';
import type { Permission, Role } from './types';

function sameSet(a: ReadonlySet<string>, b: readonly string[]): boolean {
  return a.size === b.length && b.every((value) => a.has(value));
}

type EditorProps = {
  role: Role;
  permissions: readonly Permission[];
  canManage: boolean;
  onSaved: () => void;
};

function RoleEditor({ role, permissions, canManage, onSaved }: EditorProps) {
  const { t, locale } = useI18n();
  const readOnly = role.builtIn || !canManage;
  const [name, setName] = useState(role.name);
  const [description, setDescription] = useState(role.description);
  const [selected, setSelected] = useState<Set<string>>(new Set(role.permissions));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [saved, setSaved] = useState(false);
  const [nameError, setNameError] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleteError, setDeleteError] = useState<ApiError | undefined>(undefined);
  const [deleting, setDeleting] = useState(false);

  const dirty =
    name !== role.name || description !== role.description || !sameSet(selected, role.permissions);

  const save = async (event: FormEvent) => {
    event.preventDefault();
    if (name.trim() === '') {
      setNameError(true);
      return;
    }
    setNameError(false);
    setBusy(true);
    setError(undefined);
    setSaved(false);
    try {
      if (name !== role.name || description !== role.description) {
        await accessApi.updateRole(role.id, { name: name.trim(), description });
      }
      if (!sameSet(selected, role.permissions)) {
        await accessApi.setRolePermissions(role.id, [...selected].sort());
      }
      setName(name.trim());
      setSaved(true);
      onSaved();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const remove = async () => {
    setDeleting(true);
    setDeleteError(undefined);
    try {
      await accessApi.deleteRole(role.id);
      navigate('/admin/roles');
    } catch (cause) {
      setDeleteError(asApiError(cause));
      setDeleting(false);
    }
  };

  return (
    <>
      {role.builtIn ? <Alert kind="info">{t('roles.builtIn.notice')}</Alert> : null}
      <form className="form" onSubmit={(event) => void save(event)} noValidate>
        {error ? <ApiErrorAlert error={error} /> : null}
        {saved && !dirty ? <Alert kind="success">{t('roles.saved')}</Alert> : null}
        <dl className="facts">
          <dt>{t('roles.field.key')}</dt>
          <dd>
            <code>{role.key}</code> {role.builtIn ? <Badge>{t('roles.builtIn')}</Badge> : null}
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
        <PermissionChecklist
          permissions={permissions}
          selected={selected}
          onChange={setSelected}
          disabled={readOnly}
        />
        {!readOnly ? (
          <div className="form-actions">
            <Button type="submit" variant="primary" busy={busy} disabled={!dirty}>
              {t('action.save')}
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

export function RoleDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const role = useAsync((signal) => accessApi.role(id, signal), [id]);
  const permissions = useAsync((signal) => accessApi.permissions(signal), []);

  return (
    <>
      <PageHeader
        title={role.data?.name ?? t('roles.detail.title')}
        intro={role.data?.description ? role.data.description : undefined}
        actions={
          <Link to="/admin/roles" className="btn btn-secondary">
            {t('roles.back')}
          </Link>
        }
      />
      {role.loading || permissions.loading ? <p role="status">{t('state.loading')}</p> : null}
      {role.error ? <ApiErrorAlert error={role.error} onRetry={role.reload} /> : null}
      {permissions.error ? (
        <ApiErrorAlert error={permissions.error} onRetry={permissions.reload} />
      ) : null}
      {role.data && permissions.data ? (
        <RoleEditor
          key={role.data.id}
          role={role.data}
          permissions={permissions.data.items}
          canManage={can('platform.roles.manage')}
          onSaved={role.reload}
        />
      ) : null}
    </>
  );
}
