import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Checkbox, Select } from '../../platform/ui/Field';
import { peopleAdminApi } from './adminApi';
import type { ImportBatchPreview } from './adminTypes';
import { CountBadges, PreviewNotes, PreviewRows } from './ImportPreview';
import {
  BULK_MAX_USERS,
  bulkDeactivateReasons,
  bulkDraftReady,
  bulkOperations,
  canApply,
  count,
  emptyBulkDraft,
  toBulkRequest,
  writes,
  type BulkDraft,
} from './importModel';
import { PersonPicker } from './PersonPicker';
import type { useOrgLookups } from './orgLookups';

type Lookups = ReturnType<typeof useOrgLookups>;

/**
 * Bulk edit of the selected Users (F14 design 1.6): choose the operation, review the stored dry run row by row
 * (changes, unchanged, skipped with the reason, including the dominance rule), then apply the accepted rows in one
 * transaction. Nothing is written before the last button.
 */
export function BulkDialog({
  userIds,
  lookups,
  onClose,
  onApplied,
}: {
  userIds: readonly string[];
  lookups: Lookups;
  onClose: () => void;
  onApplied: () => void;
}) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<BulkDraft>(emptyBulkDraft);
  const [preview, setPreview] = useState<ImportBatchPreview | null>(null);
  const [result, setResult] = useState<ImportBatchPreview | null>(null);
  const [reviewed, setReviewed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

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
  const close = () => {
    if (result) onApplied();
    onClose();
  };

  if (result) {
    return (
      <Dialog title={t('people.bulk.doneTitle')} onClose={close} wide>
        <Alert kind="success">{t('people.bulk.doneText')}</Alert>
        <CountBadges batch={result} applied />
        <PreviewRows preview={result} applied />
        <div className="dialog-actions">
          <Button variant="primary" onClick={close}>
            {t('action.close')}
          </Button>
        </div>
      </Dialog>
    );
  }

  if (preview) {
    const rejects = count(preview, 'reject');
    return (
      <Dialog
        title={t('people.bulk.previewTitle', { count: userIds.length })}
        onClose={onClose}
        wide
      >
        <Alert kind="info">{t('people.bulk.previewIntro')}</Alert>
        <CountBadges batch={preview} />
        <PreviewNotes preview={preview} />
        <PreviewRows preview={preview} />
        {rejects > 0 ? (
          <Checkbox
            label={t('people.bulk.reviewed', { count: rejects })}
            checked={reviewed}
            onChange={(event) => setReviewed(event.target.checked)}
          />
        ) : null}
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={() => setPreview(null)}>{t('action.back')}</Button>
          <Button
            variant={draft.operation === 'deactivate' ? 'danger' : 'primary'}
            busy={busy}
            disabled={!canApply(preview, new Date()) || (rejects > 0 && !reviewed)}
            onClick={() =>
              void run(async () => {
                setResult(await peopleAdminApi.bulkApply(preview.id, preview.previewHash, rejects));
              })
            }
          >
            {t('people.bulk.applyAction', { count: writes(preview) })}
          </Button>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog title={t('people.bulk.title', { count: userIds.length })} onClose={onClose}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (!bulkDraftReady(draft, userIds.length)) return;
          void run(async () => {
            setPreview(await peopleAdminApi.bulkPreview(toBulkRequest(draft, userIds)));
            setReviewed(false);
          });
        }}
      >
        <Select
          label={t('people.bulk.operation')}
          value={draft.operation}
          onChange={(event) =>
            setDraft({ ...emptyBulkDraft, operation: event.target.value as BulkDraft['operation'] })
          }
          options={bulkOperations.map((value) => ({ value, label: t(`people.bulk.op.${value}`) }))}
        />
        {draft.operation === 'set_department' ? (
          <Select
            label={t('people.field.department')}
            hint={t('people.bulk.clearHint')}
            value={draft.departmentId}
            onChange={(event) => setDraft({ ...draft, departmentId: event.target.value })}
            options={[
              { value: '', label: t('people.edit.none') },
              ...lookups.departments
                .filter((item) => item.active)
                .map((item) => ({
                  value: item.id,
                  label: lookups.departmentName(item.id) ?? item.name,
                })),
            ]}
          />
        ) : null}
        {draft.operation === 'set_primary_location' ? (
          <Select
            label={t('people.field.primaryLocation')}
            hint={t('people.bulk.clearHint')}
            value={draft.locationId}
            onChange={(event) => setDraft({ ...draft, locationId: event.target.value })}
            options={[
              { value: '', label: t('people.edit.none') },
              ...lookups.locations
                .filter((item) => item.active)
                .map((item) => ({
                  value: item.id,
                  label: lookups.locationName(item.id) ?? item.name,
                })),
            ]}
          />
        ) : null}
        {draft.operation === 'set_manager' ? (
          <fieldset className="adm-manager">
            <legend>{t('people.field.manager')}</legend>
            <p className="field-hint">{t('people.bulk.clearHint')}</p>
            <PersonPicker
              value={draft.manager}
              onChange={(manager) => setDraft({ ...draft, manager })}
              exclude={userIds}
            />
          </fieldset>
        ) : null}
        {draft.operation === 'deactivate' ? (
          <>
            <Alert kind="warning">{t('people.bulk.deactivateWarning')}</Alert>
            <Select
              label={t('lifecycle.reason')}
              hint={t('lifecycle.reasonHint')}
              value={draft.reason}
              onChange={(event) => setDraft({ ...draft, reason: event.target.value })}
              options={[
                { value: '', label: t('lifecycle.reason.choose') },
                ...bulkDeactivateReasons.map((code) => ({
                  value: code,
                  label: t(`lifecycle.reason.${code}`),
                })),
              ]}
              required
            />
          </>
        ) : null}
        {userIds.length > BULK_MAX_USERS ? (
          <Alert kind="warning">{t('people.bulk.tooMany', { max: BULK_MAX_USERS })}</Alert>
        ) : null}
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            variant="primary"
            type="submit"
            busy={busy}
            disabled={!bulkDraftReady(draft, userIds.length)}
          >
            {t('people.bulk.previewAction')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
