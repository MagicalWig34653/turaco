import { useEffect, useMemo, useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { isValidRoleKey } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useOptionalText } from '../../platform/i18n/optionalText';
import { useModules } from '../../platform/modules/ModulesProvider';
import { Link, navigate } from '../../platform/router/Router';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { proposeRoleKey, riskCounts } from './accessModel';
import { accessApi } from './api';
import { PermissionPicker } from './PermissionPicker';
import type { RoleCreate } from './types';
import { useActor } from './useActor';

type Mode = 'template' | 'copy' | 'empty';

export function RoleCreateScreen() {
  const { t } = useI18n();
  const text = useOptionalText();
  const { enabled } = useModules();
  const actor = useActor();
  const params = new URLSearchParams(window.location.search);
  const permissions = useAsync((signal) => accessApi.permissions(signal), []);
  const templates = useAsync(
    (signal) => accessApi.templates(signal).catch(() => ({ items: [] })),
    [],
  );
  const roles = useAsync((signal) => accessApi.roles(signal), []);

  const [mode, setMode] = useState<Mode>(
    params.get('template') ? 'template' : params.get('copy') ? 'copy' : 'template',
  );
  const [templateKey, setTemplateKey] = useState(params.get('template') ?? '');
  const [copyId, setCopyId] = useState(params.get('copy') ?? '');
  const [name, setName] = useState('');
  const [key, setKey] = useState('');
  const [keyEdited, setKeyEdited] = useState(false);
  const [description, setDescription] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [touched, setTouched] = useState(false);
  const [confirming, setConfirming] = useState(false);

  const template = templates.data?.items.find((entry) => entry.key === templateKey);
  const source = roles.data?.items.find((role) => role.id === copyId);
  const byName = useMemo(
    () =>
      new Map((permissions.data?.items ?? []).map((permission) => [permission.name, permission])),
    [permissions.data],
  );
  const shown =
    mode === 'template'
      ? (template?.permissions ?? [])
      : mode === 'copy'
        ? (source?.permissions ?? [])
        : [...selected];
  const counts = riskCounts(shown, byName);

  // A template opened from the gallery proposes its name and key once its data is there.
  useEffect(() => {
    if (!template || name !== '' || keyEdited) return;
    const proposed = text(template.nameKey, template.name);
    setName(proposed);
    setKey(proposeRoleKey(`${proposed} custom`));
  }, [template?.key]);

  const keyError = touched && !isValidRoleKey(key) ? t('roles.key.invalid') : undefined;
  const nameError = touched && name.trim() === '' ? t('roles.name.required') : undefined;
  const sourceMissing = (mode === 'template' && !template) || (mode === 'copy' && !source);

  const body = (): RoleCreate => ({
    key,
    name: name.trim(),
    description,
    ...(mode === 'template'
      ? { templateKey }
      : mode === 'copy'
        ? { cloneFromRoleId: copyId }
        : { permissions: [...selected].sort() }),
  });

  const review = () => {
    setTouched(true);
    if (!isValidRoleKey(key) || name.trim() === '' || sourceMissing) return;
    setConfirming(true);
  };

  const pickTemplate = (next: string) => {
    setTemplateKey(next);
    const entry = templates.data?.items.find((candidate) => candidate.key === next);
    if (entry && !name) {
      const proposed = text(entry.nameKey, entry.name);
      setName(proposed);
      if (!keyEdited) setKey(proposeRoleKey(`${proposed} custom`));
    }
  };

  return (
    <>
      <PageHeader
        title={t('roles.create.title')}
        eyebrow={t('nav.roles')}
        intro={t('roles.create.intro')}
        actions={
          <Link to="/admin/roles" className="btn btn-secondary">
            {t('roles.back')}
          </Link>
        }
      />
      {permissions.error ? (
        <ApiErrorAlert error={permissions.error} onRetry={permissions.reload} />
      ) : null}
      <form
        className="form adm-wide-form"
        onSubmit={(event) => {
          event.preventDefault();
          review();
        }}
        noValidate
      >
        <fieldset className="adm-mode">
          <legend>{t('roles.create.mode')}</legend>
          {(['template', 'copy', 'empty'] as const).map((value) => (
            <label key={value} className="adm-radio-card">
              <input
                type="radio"
                name="mode"
                checked={mode === value}
                onChange={() => setMode(value)}
              />
              <span>
                <strong>{t(`roles.create.mode.${value}`)}</strong>
                <span className="adm-sub">{t(`roles.create.mode.${value}.hint`)}</span>
              </span>
            </label>
          ))}
        </fieldset>

        {mode === 'template' ? (
          <>
            <Select
              label={t('roles.create.template')}
              value={templateKey}
              onChange={(event) => pickTemplate(event.target.value)}
              options={[
                { value: '', label: t('roles.create.template.choose') },
                ...(templates.data?.items ?? []).map((entry) => ({
                  value: entry.key,
                  label: text(entry.nameKey, entry.name),
                })),
              ]}
            />
            {!templates.loading && (templates.data?.items.length ?? 0) === 0 ? (
              <Alert kind="info">{t('roles.templates.unavailable')}</Alert>
            ) : null}
            {template?.administratorAssignOnly ? (
              <Alert kind="warning">{t('roles.create.administratorOnly')}</Alert>
            ) : null}
            {template?.externalOnly ? (
              <Alert kind="warning">{t('roles.create.externalOnly')}</Alert>
            ) : null}
            {template ? (
              <p className="field-hint">{text(template.descriptionKey, template.description)}</p>
            ) : null}
          </>
        ) : null}
        {mode === 'copy' ? (
          <Select
            label={t('roles.create.copy')}
            value={copyId}
            onChange={(event) => setCopyId(event.target.value)}
            options={[
              { value: '', label: t('roles.create.copy.choose') },
              ...(roles.data?.items ?? []).map((role) => ({
                value: role.id,
                label: `${role.name} (${role.key})`,
              })),
            ]}
          />
        ) : null}

        <TextField
          label={t('roles.field.name')}
          value={name}
          error={nameError}
          maxLength={200}
          required
          onChange={(event) => {
            setName(event.target.value);
            if (!keyEdited) setKey(proposeRoleKey(event.target.value));
          }}
        />
        <TextField
          label={t('roles.field.key')}
          hint={t('roles.key.hint')}
          value={key}
          error={keyError}
          maxLength={63}
          required
          onChange={(event) => {
            setKey(event.target.value);
            setKeyEdited(true);
          }}
        />
        <TextArea
          label={t('roles.field.description')}
          value={description}
          maxLength={2000}
          rows={3}
          onChange={(event) => setDescription(event.target.value)}
        />

        <h2>{t('roles.field.permissions')}</h2>
        {mode === 'empty' ? (
          <p className="field-hint">{t('roles.create.empty.hint')}</p>
        ) : (
          <p className="field-hint">{t('roles.create.prefill.hint')}</p>
        )}
        {permissions.data ? (
          <PermissionPicker
            key={`${mode}:${templateKey}:${copyId}`}
            permissions={permissions.data.items}
            selected={mode === 'empty' ? selected : new Set(shown)}
            onChange={setSelected}
            disabled={mode !== 'empty'}
            initialOnlySelected={mode !== 'empty'}
            actor={actor}
            moduleEnabled={enabled}
          />
        ) : null}
        <div className="form-actions">
          <Button type="submit" variant="primary">
            {t('roles.create.review')}
          </Button>
        </div>
      </form>
      {confirming ? (
        <GuardedActionDialog
          title={t('roles.create.confirmTitle', { name: name.trim() })}
          confirmLabel={t('roles.create.submit')}
          run={async (extras) => {
            const role = await accessApi.createRole({ ...body(), ...extras });
            navigate(`/admin/roles/${encodeURIComponent(role.id)}`);
          }}
          onDone={() => undefined}
          onClose={() => setConfirming(false)}
        >
          <p>
            {t('roles.create.summary', {
              count: shown.length,
              elevated: counts.elevated,
              high: counts.high,
            })}
          </p>
          <p className="field-hint">{t('roles.create.noAssignment')}</p>
        </GuardedActionDialog>
      ) : null}
    </>
  );
}
