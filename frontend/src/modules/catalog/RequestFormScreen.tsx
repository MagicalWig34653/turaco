import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { requestsApi } from '../requests/api';
import { AssigneePicker, type Assignee } from '../tasks/AssigneePicker';
import { PersonLookup } from '../organization/PersonLookup';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { previewState, stepText } from './approvalModel';
import { approvalPreview, catalogApi } from './api';
import { fieldErrorKey, initialValues, toAnswers, type FormValues } from './formModel';
import type { CatalogForm, FormField } from './types';

function UserField({
  field,
  error,
  onChange,
}: {
  field: FormField;
  error: string | undefined;
  onChange: (id: string) => void;
}) {
  const [selected, setSelected] = useState<Assignee | null>(null);
  return (
    <fieldset className="field">
      <legend>
        {field.label}
        {field.required ? ' *' : ''}
      </legend>
      {field.help ? <p className="field-hint">{field.help}</p> : null}
      <AssigneePicker
        type="user"
        value={selected}
        onChange={(next) => {
          setSelected(next);
          onChange(next?.id ?? '');
        }}
      />
      {error ? <p className="field-error">{error}</p> : null}
    </fieldset>
  );
}

/** Who approves this request, before it is sent; hidden when the server cannot tell. */
function ApprovalRoute({
  itemId,
  requestedForId,
  onBlocked,
}: {
  itemId: string;
  requestedForId: string | undefined;
  onBlocked: (blocked: boolean) => void;
}) {
  const { t } = useI18n();
  const preview = useAsync(
    (signal) => approvalPreview(itemId, requestedForId, signal),
    [itemId, requestedForId],
  );
  const blocked = previewState(preview.data) === 'blocked';
  useEffect(() => {
    onBlocked(blocked);
  }, [blocked, onBlocked]);
  if (preview.loading && !preview.data) return <Skeleton lines={2} />;
  if (preview.error) {
    // The preview is advice; a failure must not stop the request.
    return (
      <p className="field-hint" role="status">
        {t('catalog.approval.unavailable')}
      </p>
    );
  }
  const state = previewState(preview.data);
  if (state === 'unknown') return null;
  return (
    <Card className="catalog-approval" title={t('catalog.approval.title')}>
      <h2>{t('catalog.approval.title')}</h2>
      {state === 'none' ? <p>{t('catalog.approval.none')}</p> : null}
      {preview.data && state !== 'none' ? (
        <ol className="catalog-approval-steps">
          {preview.data.steps.map((step) => {
            const text = stepText(step);
            return (
              <li key={step.index} className={step.resolved ? '' : 'is-unresolved'}>
                {t(text.key, text.params)}
              </li>
            );
          })}
        </ol>
      ) : null}
      {state === 'blocked' ? (
        <Alert kind="warning">
          <p>
            <strong>{t('catalog.approval.blockedTitle')}</strong>
          </p>
          <p>{t('catalog.approval.blocked')}</p>
          <p>
            <Link to="/support/new">{t('catalog.approval.contact')}</Link>
          </p>
        </Alert>
      ) : null}
    </Card>
  );
}

function FieldInput({
  field,
  value,
  error,
  onChange,
}: {
  field: FormField;
  value: string | boolean | undefined;
  error: string | undefined;
  onChange: (value: string | boolean) => void;
}) {
  const { t } = useI18n();
  const label = field.required ? `${field.label} *` : field.label;
  const common = { label, hint: field.help, error };
  const text = typeof value === 'string' ? value : '';
  switch (field.type) {
    case 'boolean':
      return (
        <Checkbox
          label={field.label}
          description={field.help}
          checked={value === true}
          onChange={(event) => onChange(event.target.checked)}
        />
      );
    case 'longtext':
      return (
        <TextArea
          {...common}
          value={text}
          rows={4}
          maxLength={field.maxLength}
          onChange={(event) => onChange(event.target.value)}
        />
      );
    case 'number':
      return (
        <TextField
          {...common}
          value={text}
          inputMode="numeric"
          onChange={(event) => onChange(event.target.value)}
        />
      );
    case 'date':
      return (
        <TextField
          {...common}
          type="date"
          value={text}
          onChange={(event) => onChange(event.target.value)}
        />
      );
    case 'select':
    case 'product': {
      const options =
        field.type === 'select'
          ? (field.options ?? [])
          : (field.productOptions ?? []).map((p) => ({ value: p.id, label: p.name }));
      return (
        <Select
          {...common}
          value={text}
          onChange={(event) => onChange(event.target.value)}
          options={[{ value: '', label: t('catalog.form.choose') }, ...options]}
        />
      );
    }
    case 'user':
      return <UserField field={field} error={error} onChange={onChange} />;
    default:
      return (
        <TextField
          {...common}
          value={text}
          maxLength={field.maxLength}
          onChange={(event) => onChange(event.target.value)}
        />
      );
  }
}

export function RequestFormScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const loaded = useAsync((signal) => catalogApi.form(id, signal), [id]);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const form = loaded.data;
  if (!form) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  return <RequestForm form={form} />;
}

function RequestForm({ form }: { form: CatalogForm }) {
  const { t } = useI18n();
  const [values, setValues] = useState<FormValues>(() => initialValues(form.fields));
  const [requestedFor, setRequestedFor] = useState<Assignee | null>(null);
  const [forOther, setForOther] = useState(false);
  const [blocked, setBlocked] = useState(false);
  const previewFor = useDebouncedValue(forOther ? requestedFor?.id : undefined, 300);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [busy, setBusy] = useState(false);

  const message = (code: string) => t(fieldErrorKey(code) as MessageKey);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const { answers, errors } = toAnswers(form.fields, values);
    setError(undefined);
    if (Object.keys(errors).length > 0) {
      setFieldErrors(errors);
      return;
    }
    setFieldErrors({});
    setBusy(true);
    try {
      const created = await requestsApi.submit({
        catalogItemId: form.id,
        ...(forOther && requestedFor ? { requestedForId: requestedFor.id } : {}),
        answers,
      });
      navigate(`/requests/${encodeURIComponent(created.id)}`);
    } catch (cause) {
      const apiError = asApiError(cause);
      setError(apiError);
      setFieldErrors({ ...apiError.fields });
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={form.title} intro={form.description || undefined} />
      <p>
        <Link to="/catalog">{t('catalog.back')}</Link>
      </p>
      <form className="form" onSubmit={(event) => void submit(event)} noValidate>
        {error ? <ApiErrorAlert error={error} /> : null}
        {Object.keys(fieldErrors).length > 0 ? (
          <Alert kind="error">{t('catalog.form.fixErrors')}</Alert>
        ) : null}
        {form.allowRequestedFor ? (
          <fieldset className="field">
            <legend>{t('catalog.form.requestedFor')}</legend>
            <Checkbox
              label={t('catalog.form.forOther')}
              checked={forOther}
              onChange={(event) => {
                setForOther(event.target.checked);
                if (!event.target.checked) setRequestedFor(null);
              }}
            />
            {forOther ? (
              <PersonLookup
                label={t('catalog.form.requestedFor.search')}
                hint={t('catalog.form.requestedFor.hint')}
                value={requestedFor}
                onChange={setRequestedFor}
              />
            ) : (
              <p className="field-hint">{t('catalog.form.forMe')}</p>
            )}
            {fieldErrors.requestedForId ? (
              <p className="field-error">{message(fieldErrors.requestedForId)}</p>
            ) : null}
          </fieldset>
        ) : null}
        <ApprovalRoute itemId={form.id} requestedForId={previewFor} onBlocked={setBlocked} />
        {form.fields.map((field) => (
          <FieldInput
            key={field.key}
            field={field}
            value={values[field.key]}
            error={fieldErrors[field.key] ? message(fieldErrors[field.key] ?? '') : undefined}
            onChange={(value) => setValues((previous) => ({ ...previous, [field.key]: value }))}
          />
        ))}
        <div className="form-actions">
          <Button
            type="submit"
            variant="primary"
            busy={busy}
            disabled={blocked || (forOther && !requestedFor)}
          >
            {t('catalog.form.submit')}
          </Button>
        </div>
      </form>
    </>
  );
}
