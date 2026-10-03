import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { TextArea, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { runbooksApi, type Runbook, type RunbookStep } from './api';

function Form({ runbook }: { runbook?: Runbook | undefined }) {
  const { t } = useI18n();
  const [title, setTitle] = useState(runbook?.title ?? '');
  const [description, setDescription] = useState(runbook?.description ?? '');
  const [steps, setSteps] = useState<RunbookStep[]>(runbook?.steps ?? [{ title: '' }]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const setStep = (i: number, patch: Partial<RunbookStep>) =>
    setSteps((s) => s.map((x, j) => (j === i ? { ...x, ...patch } : x)));
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    const body = {
      title: title.trim(),
      description: description.trim(),
      steps: steps.map((s) => ({
        title: s.title.trim(),
        description: (s.description ?? '').trim(),
      })),
    };
    try {
      const saved = runbook
        ? await runbooksApi.update(runbook.id, runbook.version, body)
        : await runbooksApi.create(body);
      navigate(`/runbooks/${encodeURIComponent(saved.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <form className="form" onSubmit={(event) => void submit(event)}>
      {error ? <ApiErrorAlert error={error} /> : null}
      <TextField
        label={t('runbooks.field.title')}
        value={title}
        maxLength={200}
        required
        onChange={(event) => setTitle(event.target.value)}
      />
      <TextArea
        label={t('runbooks.field.description')}
        value={description}
        rows={3}
        maxLength={2000}
        onChange={(event) => setDescription(event.target.value)}
      />
      {steps.map((step, i) => (
        <fieldset key={i} className="field">
          <legend>{t('runbooks.step', { n: i + 1 })}</legend>
          <TextField
            label={t('runbooks.field.stepTitle')}
            value={step.title}
            maxLength={150}
            required
            onChange={(event) => setStep(i, { title: event.target.value })}
          />
          <TextArea
            label={t('runbooks.field.stepDescription')}
            value={step.description ?? ''}
            rows={2}
            maxLength={2000}
            onChange={(event) => setStep(i, { description: event.target.value })}
          />
          <Button
            onClick={() => setSteps((s) => s.filter((_, j) => j !== i))}
            disabled={steps.length <= 1}
          >
            {t('runbooks.removeStep')}
          </Button>
        </fieldset>
      ))}
      <div className="form-actions">
        <Button
          onClick={() => setSteps((s) => [...s, { title: '' }])}
          disabled={steps.length >= 30}
        >
          {t('runbooks.addStep')}
        </Button>
        <Button
          type="submit"
          variant="primary"
          busy={busy}
          disabled={title.trim() === '' || steps.some((s) => s.title.trim() === '')}
        >
          {t('runbooks.save')}
        </Button>
      </div>
    </form>
  );
}

export function RunbookEditScreen({ id }: { id?: string }) {
  const { t } = useI18n();
  const loaded = useAsync(
    (signal) => (id ? runbooksApi.get(id, signal) : Promise.resolve(undefined)),
    [id],
  );
  return (
    <>
      <PageHeader
        title={t(id ? 'runbooks.edit' : 'runbooks.new')}
        intro={t('runbooks.edit.hint')}
      />
      <p>
        <Link to={id ? `/runbooks/${encodeURIComponent(id)}` : '/runbooks'}>
          {t('runbooks.back')}
        </Link>
      </p>
      {loaded.error ? <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} /> : null}
      {!loaded.loading && !loaded.error ? <Form key={id ?? 'new'} runbook={loaded.data} /> : null}
    </>
  );
}
