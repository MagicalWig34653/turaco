import { useState, type FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDate } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Card, Skeleton } from '../../platform/ui/Workspace';
import { timeTrackingApi } from './api';
import type { TimeTrackingSettings } from './types';
import './timetracking.css';

/**
 * Time Tracking settings (timetracking.admin, F17 T-A). Recording the co-determination basis is the precondition
 * for switching the module on in the module overview; the screen stays reachable while the module is off.
 */
export function TimeTrackingSettingsScreen() {
  const { t } = useI18n();
  const loaded = useAsync((signal) => timeTrackingApi.settings(signal), []);
  return (
    <>
      <PageHeader
        eyebrow={t('timetracking.eyebrow')}
        title={t('timetracking.settings')}
        intro={t('timetracking.settingsIntro')}
      />
      {loaded.error ? (
        <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />
      ) : !loaded.data ? (
        <Skeleton lines={5} />
      ) : (
        <SettingsForm key={loaded.data.version} settings={loaded.data} onSaved={loaded.reload} />
      )}
    </>
  );
}

function SettingsForm({
  settings,
  onSaved,
}: {
  settings: TimeTrackingSettings;
  onSaved: () => void;
}) {
  const { t, locale } = useI18n();
  const [basis, setBasis] = useState(settings.codeterminationBasis ?? '');
  const [basisOn, setBasisOn] = useState(settings.codeterminationRecordedOn ?? '');
  const [dpia, setDpia] = useState(settings.dpiaRecordedOn ?? '');
  const [window, setWindow] = useState(String(settings.editWindowDays));
  const [retention, setRetention] = useState(String(settings.retentionMonths));
  const [authors, setAuthors] = useState(settings.showEntryAuthors);
  const [group, setGroup] = useState(String(settings.minReportGroupSize));
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const [error, setError] = useState<ApiError>();
  const recorded = settings.codeterminationBasis !== null;
  const invalid =
    (basis !== '' && basisOn === '') ||
    (basis === '' && recorded) ||
    Number(window) < 1 ||
    Number(window) > 90 ||
    Number(retention) < 3 ||
    Number(retention) > 120 ||
    Number(group) < 2 ||
    Number(group) > 20;
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setSaved(false);
    setError(undefined);
    try {
      await timeTrackingApi.saveSettings({
        ...(basis ? { codeterminationBasis: basis as 'agreement' | 'not_applicable' } : {}),
        ...(basis && basisOn ? { codeterminationRecordedOn: basisOn } : {}),
        ...(dpia ? { dpiaRecordedOn: dpia } : {}),
        editWindowDays: Number(window),
        retentionMonths: Number(retention),
        showEntryAuthors: authors,
        minReportGroupSize: Number(group),
        expectedVersion: settings.version,
      });
      setSaved(true);
      onSaved();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Card title={t('timetracking.settings')}>
      <Alert kind="info">
        <p>{t('timetracking.privacy')}</p>
      </Alert>
      {!recorded ? (
        <Alert kind="warning">
          <p>{t('timetracking.codetermination.missing')}</p>
        </Alert>
      ) : (
        <p>
          {t('timetracking.codetermination.recorded', {
            basis: t(`timetracking.basis.${settings.codeterminationBasis!}`),
            date: formatDate(locale, settings.codeterminationRecordedOn),
          })}
        </p>
      )}
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('timetracking.codetermination')}
          hint={t('timetracking.codeterminationHint')}
          value={basis}
          onChange={(event) => setBasis(event.target.value as '' | 'agreement' | 'not_applicable')}
          options={[
            ...(recorded ? [] : [{ value: '', label: t('timetracking.basis.none') }]),
            { value: 'agreement', label: t('timetracking.basis.agreement') },
            { value: 'not_applicable', label: t('timetracking.basis.not_applicable') },
          ]}
        />
        <TextField
          label={t('timetracking.codeterminationDate')}
          type="date"
          value={basisOn}
          onChange={(event) => setBasisOn(event.target.value)}
          required={basis !== ''}
          disabled={basis === ''}
        />
        <TextField
          label={t('timetracking.dpiaDate')}
          type="date"
          value={dpia}
          onChange={(event) => setDpia(event.target.value)}
        />
        <TextField
          label={t('timetracking.editWindow')}
          hint={t('timetracking.editWindowHint')}
          type="number"
          min={1}
          max={90}
          value={window}
          onChange={(event) => setWindow(event.target.value)}
          required
        />
        <TextField
          label={t('timetracking.retention')}
          hint={t('timetracking.retentionHint')}
          type="number"
          min={3}
          max={120}
          value={retention}
          onChange={(event) => setRetention(event.target.value)}
          required
        />
        <Checkbox
          label={t('timetracking.showAuthors')}
          description={t('timetracking.showAuthorsHint')}
          checked={authors}
          onChange={(event) => setAuthors(event.target.checked)}
        />
        <TextField
          label={t('timetracking.groupSize')}
          hint={t('timetracking.groupSizeHint')}
          type="number"
          min={2}
          max={20}
          value={group}
          onChange={(event) => setGroup(event.target.value)}
          required
        />
        <div className="dialog-actions">
          <Button type="submit" variant="primary" busy={busy} disabled={invalid}>
            {t('action.save')}
          </Button>
        </div>
        {saved ? <p role="status">{t('timetracking.saved')}</p> : null}
      </form>
    </Card>
  );
}
