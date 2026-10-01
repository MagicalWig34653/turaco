import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { isValidRoleKey } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { PermissionChecklist } from './PermissionChecklist';
import { accessApi } from './api';

export function RoleCreateScreen() {
  const { t } = useI18n();
  const permissions = useAsync((signal) => accessApi.permissions(signal), []);
  const [key, setKey] = useState('');
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [touched, setTouched] = useState(false);

  const keyError = touched && !isValidRoleKey(key) ? t('roles.key.invalid') : undefined;
  const nameError = touched && name.trim() === '' ? t('roles.name.required') : undefined;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setTouched(true);
    if (!isValidRoleKey(key) || name.trim() === '') return;
    setBusy(true);
    setError(undefined);
    try {
      const role = await accessApi.createRole({
        key,
        name: name.trim(),
        description,
        permissions: [...selected].sort(),
      });
      navigate(`/admin/roles/${encodeURIComponent(role.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={t('roles.create.title')} intro={t('roles.create.intro')} />
      <form className="form" onSubmit={(event) => void submit(event)} noValidate>
        {error ? <ApiErrorAlert error={error} /> : null}
        <TextField
          label={t('roles.field.key')}
          hint={t('roles.key.hint')}
          error={keyError}
          value={key}
          onChange={(event) => setKey(event.target.value)}
          maxLength={64}
          spellCheck={false}
          autoCapitalize="none"
          required
        />
        <TextField
          label={t('roles.field.name')}
          error={nameError}
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={200}
          required
        />
        <TextArea
          label={t('roles.field.description')}
          value={description}
          onChange={(event) => setDescription(event.target.value)}
          maxLength={2000}
          rows={3}
        />
        <h2>{t('roles.field.permissions')}</h2>
        {permissions.loading ? <p role="status">{t('state.loading')}</p> : null}
        {permissions.error ? (
          <ApiErrorAlert error={permissions.error} onRetry={permissions.reload} />
        ) : null}
        {permissions.data ? (
          <PermissionChecklist
            permissions={permissions.data.items}
            selected={selected}
            onChange={setSelected}
          />
        ) : null}
        <div className="form-actions">
          <Button type="submit" variant="primary" busy={busy}>
            {t('roles.create.submit')}
          </Button>
          <Link to="/admin/roles" className="btn btn-secondary">
            {t('action.cancel')}
          </Link>
        </div>
      </form>
    </>
  );
}
