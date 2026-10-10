import { useRef, useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { peopleAdminApi } from './adminApi';
import type { ImportBatchPreview, ImportKind, ImportMatchKey, ImportMode } from './adminTypes';
import { CountBadges, PreviewNotes, PreviewRows } from './ImportPreview';
import {
  IMPORT_MAX_BYTES,
  IMPORT_MAX_ROWS,
  canApply,
  checkImportFile,
  count,
  importColumns,
  importKinds,
  importModes,
  matchKeysFor,
  writes,
} from './importModel';

type Step = 'upload' | 'preview' | 'done';

const listRoute: Record<ImportKind, string> = {
  users: '/admin/users',
  locations: '/admin/locations',
  departments: '/admin/departments',
};

/**
 * CSV import wizard (F14 design 1.6): upload, dry-run preview with per-row findings, explicit confirmation. The
 * preview is stored server-side for an hour; nothing is written before the last step.
 */
export function ImportScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const kinds = importKinds(can);
  const [step, setStep] = useState<Step>('upload');
  const [kind, setKind] = useState<ImportKind>(kinds[0] ?? 'users');
  const [matchKey, setMatchKey] = useState<ImportMatchKey>('primary_email');
  const [mode, setMode] = useState<ImportMode>('upsert');
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<ImportBatchPreview | null>(null);
  const [applied, setApplied] = useState<ImportBatchPreview | null>(null);
  const [reviewed, setReviewed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const fileInput = useRef<HTMLInputElement>(null);

  const fileCheck = checkImportFile(file);
  const keys = matchKeysFor(kind);

  const changeKind = (next: ImportKind) => {
    setKind(next);
    setMatchKey(matchKeysFor(next)[0] ?? 'code');
  };

  const run = async (action: () => Promise<void>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  const reset = () => {
    setStep('upload');
    setPreview(null);
    setApplied(null);
    setReviewed(false);
    setFile(null);
    setError(undefined);
    if (fileInput.current) fileInput.current.value = '';
  };

  const header = (
    <PageHeader
      title={t('people.import.title')}
      eyebrow={t('sidebar.section.admin')}
      intro={t('people.import.intro')}
      actions={
        <Link to="/admin/users" className="btn btn-secondary">
          {t('people.back')}
        </Link>
      }
    />
  );

  if (kinds.length === 0) {
    return (
      <>
        {header}
        <Alert kind="warning">{t('people.import.noPermission')}</Alert>
      </>
    );
  }

  const steps: { id: Step; label: string }[] = [
    { id: 'upload', label: t('people.import.step.upload') },
    { id: 'preview', label: t('people.import.step.preview') },
    { id: 'done', label: t('people.import.step.done') },
  ];
  const currentIndex = steps.findIndex((entry) => entry.id === step);

  return (
    <>
      {header}
      <ol className="adm-steps" aria-label={t('people.create.steps')}>
        {steps.map((entry, index) => (
          <li
            key={entry.id}
            className={
              index === currentIndex ? 'is-current' : index < currentIndex ? 'is-done' : ''
            }
            aria-current={index === currentIndex ? 'step' : undefined}
          >
            <span className="adm-step-number">{index + 1}</span> {entry.label}
          </li>
        ))}
      </ol>

      {step === 'upload' ? (
        <form
          className="adm-card adm-wide-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (!file || fileCheck !== 'ok') return;
            void run(async () => {
              const result = await peopleAdminApi.previewImport(kind, matchKey, mode, file);
              setPreview(result);
              setReviewed(false);
              setStep('preview');
            });
          }}
        >
          <Select
            label={t('people.import.kind')}
            value={kind}
            onChange={(event) => changeKind(event.target.value as ImportKind)}
            options={kinds.map((value) => ({ value, label: t(`people.import.kind.${value}`) }))}
          />
          <Select
            label={t('people.import.matchKey')}
            hint={t('people.import.matchKeyHint')}
            value={matchKey}
            disabled={keys.length === 1}
            onChange={(event) => setMatchKey(event.target.value as ImportMatchKey)}
            options={keys.map((value) => ({ value, label: t(`people.import.matchKey.${value}`) }))}
          />
          <Select
            label={t('people.import.mode')}
            value={mode}
            onChange={(event) => setMode(event.target.value as ImportMode)}
            options={importModes.map((value) => ({
              value,
              label: t(`people.import.mode.${value}`),
            }))}
          />
          <div className="field">
            <label htmlFor="import-file">{t('people.import.file')}</label>
            <input
              id="import-file"
              ref={fileInput}
              type="file"
              accept=".csv,.txt,text/csv"
              aria-invalid={file !== null && fileCheck !== 'ok' ? true : undefined}
              aria-describedby="import-file-hint"
              onChange={(event) => setFile(event.target.files?.[0] ?? null)}
            />
            <p className="field-hint" id="import-file-hint">
              {t('people.import.fileHint', {
                size: IMPORT_MAX_BYTES / (1024 * 1024),
                rows: IMPORT_MAX_ROWS,
              })}
            </p>
            {file && fileCheck !== 'ok' ? (
              <p className="field-error">{t(`people.import.fileError.${fileCheck}`)}</p>
            ) : null}
          </div>
          <Alert kind="info">
            {t('people.import.columns', { columns: importColumns[kind].join(', ') })}
          </Alert>
          <Alert kind="info">{t('people.import.rules')}</Alert>
          {error ? <ApiErrorAlert error={error} /> : null}
          <div className="adm-actions">
            <Button variant="primary" type="submit" busy={busy} disabled={fileCheck !== 'ok'}>
              {t('people.import.previewAction')}
            </Button>
          </div>
        </form>
      ) : null}

      {step === 'preview' && preview ? (
        <section className="adm-card" aria-labelledby="import-preview-title">
          <h2 id="import-preview-title">{t('people.import.previewTitle')}</h2>
          <CountBadges batch={preview} />
          <PreviewNotes preview={preview} />
          {count(preview, 'reject') > 0 ? (
            <p>
              <a href={peopleAdminApi.rejectedCsvPath(preview.id)} download>
                {t('people.import.downloadRejected')}
              </a>
            </p>
          ) : null}
          <PreviewRows preview={preview} />
          {error ? <ApiErrorAlert error={error} /> : null}
          {writes(preview) === 0 ? (
            <Alert kind="info">{t('people.import.nothingToApply')}</Alert>
          ) : null}
          {count(preview, 'reject') > 0 ? (
            <Checkbox
              label={t('people.import.reviewed', { count: count(preview, 'reject') })}
              checked={reviewed}
              onChange={(event) => setReviewed(event.target.checked)}
            />
          ) : null}
          <div className="adm-actions">
            <Button
              variant="primary"
              busy={busy}
              disabled={
                !canApply(preview, new Date()) || (count(preview, 'reject') > 0 && !reviewed)
              }
              onClick={() =>
                void run(async () => {
                  setApplied(
                    await peopleAdminApi.applyImport(
                      preview.id,
                      preview.previewHash,
                      count(preview, 'reject'),
                    ),
                  );
                  setStep('done');
                })
              }
            >
              {t('people.import.applyAction', { count: writes(preview) })}
            </Button>
            <Button onClick={reset}>{t('people.import.startOver')}</Button>
          </div>
        </section>
      ) : null}

      {step === 'done' && applied ? (
        <section className="adm-card" aria-labelledby="import-done-title">
          <h2 id="import-done-title">{t('people.import.doneTitle')}</h2>
          <Alert kind="success">
            {t(applied.replayed ? 'people.import.doneReplayed' : 'people.import.doneText')}
          </Alert>
          <CountBadges batch={applied} applied />
          <div className="adm-actions">
            <Link to={listRoute[kind]} className="btn btn-primary">
              {t(`people.import.open.${kind}`)}
            </Link>
            <Button onClick={reset}>{t('people.import.another')}</Button>
          </div>
        </section>
      ) : null}
    </>
  );
}
