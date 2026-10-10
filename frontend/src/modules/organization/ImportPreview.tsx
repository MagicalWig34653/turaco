import { useState } from 'react';
import { asApiError } from '../../platform/api/useAsync';
import type { ApiError } from '../../platform/api/client';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select } from '../../platform/ui/Field';
import { formatDateTime } from '../../platform/format/format';
import { peopleAdminApi } from './adminApi';
import type { ImportBatchPreview, ImportRow, RowAction } from './adminTypes';
import { count, issueKey, rowActions } from './importModel';

const tone = (action: RowAction, applied: boolean) =>
  action === 'reject'
    ? applied
      ? 'warning'
      : 'danger'
    : action === 'unchanged'
      ? 'neutral'
      : 'success';

/** Counts of a preview or an applied batch, as text next to a tone badge (never color alone). */
export function CountBadges({
  batch,
  applied = false,
}: {
  batch: Pick<ImportBatchPreview, 'counts' | 'appliedCounts' | 'kind'>;
  applied?: boolean;
}) {
  const { t } = useI18n();
  const source = applied && batch.appliedCounts ? { counts: batch.appliedCounts } : batch;
  const bulk = batch.kind === 'bulk_users';
  return (
    <div className="adm-chip-row" aria-label={t('people.import.counts')}>
      {rowActions.map((action) => (
        <Badge key={action} tone={tone(action, applied)}>
          {t(`people.import.count.${bulk && action === 'reject' ? 'skipped' : action}`, {
            count: count(source, action),
          })}
        </Badge>
      ))}
    </div>
  );
}

function IssueList({ issues }: { issues: ImportRow['errors'] }) {
  const { t } = useI18n();
  return (
    <ul className="adm-issues">
      {issues.map((issue, index) => (
        <li key={`${issue.code}-${issue.field ?? ''}-${index}`}>
          {issue.field ? <code>{issue.field}</code> : null} {t(issueKey(issue.code))}
        </li>
      ))}
    </ul>
  );
}

/**
 * The rows of a stored preview with an action filter. The first page arrives with the preview; further pages
 * and filters read `GET /import-batches/{id}/rows` (a stored result, not a query).
 */
export function PreviewRows({
  preview,
  applied = false,
}: {
  preview: ImportBatchPreview;
  applied?: boolean;
}) {
  const { t } = useI18n();
  const [action, setAction] = useState('');
  const [rows, setRows] = useState<ImportRow[]>(preview.rows);
  const [cursor, setCursor] = useState<string | undefined>(preview.nextCursor);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const bulk = preview.kind === 'bulk_users';
  // An applied batch has no stored rows any more; its result rows came with the apply response.
  const canPage = preview.status === 'previewed';

  const load = async (nextAction: string, from: string | undefined) => {
    setLoading(true);
    setError(undefined);
    try {
      const page = await peopleAdminApi.importRows(preview.id, nextAction, from);
      setRows((current) => (from ? [...current, ...page.items] : page.items));
      setCursor(page.nextCursor || undefined);
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setLoading(false);
    }
  };

  const visible = canPage ? rows : preview.rows.filter((row) => !action || row.action === action);
  const columns: Column<ImportRow>[] = [
    { key: 'row', header: t('people.import.col.row'), render: (row) => row.row },
    {
      key: 'subject',
      header: t(bulk ? 'people.import.col.person' : 'people.import.col.key'),
      render: (row) => (bulk ? (row.label ?? row.key) : row.key),
    },
    {
      key: 'result',
      header: t('people.import.col.result'),
      render: (row) => (
        <Badge tone={tone(row.action, applied)}>
          {t(`people.import.action.${bulk && row.action === 'reject' ? 'skipped' : row.action}`)}
        </Badge>
      ),
    },
    {
      key: 'changes',
      header: t('people.import.col.changes'),
      render: (row) => {
        const entries = Object.entries(row.diff);
        if (entries.length === 0) return '–';
        return (
          <ul className="adm-issues">
            {entries.map(([field, value]) => (
              <li key={field}>
                <code>{field}</code> {value.from ?? '–'} → {value.to ?? '–'}
              </li>
            ))}
          </ul>
        );
      },
    },
    {
      key: 'notes',
      header: t('people.import.col.notes'),
      render: (row) =>
        row.errors.length + row.warnings.length === 0 ? (
          '–'
        ) : (
          <>
            {row.errors.length > 0 ? <IssueList issues={row.errors} /> : null}
            {row.warnings.length > 0 ? <IssueList issues={row.warnings} /> : null}
          </>
        ),
    },
  ];

  return (
    <>
      <Select
        label={t('people.import.filter')}
        value={action}
        onChange={(event) => {
          setAction(event.target.value);
          if (canPage) void load(event.target.value, undefined);
        }}
        options={[
          { value: '', label: t('people.import.filter.all') },
          ...rowActions.map((value) => ({
            value,
            label: t(`people.import.action.${bulk && value === 'reject' ? 'skipped' : value}`),
          })),
        ]}
      />
      {error ? <ApiErrorAlert error={error} onRetry={() => void load(action, undefined)} /> : null}
      <DataTable
        caption={t('people.import.rows')}
        columns={columns}
        rows={visible}
        rowKey={(row) => String(row.row)}
        loading={loading && visible.length === 0}
        emptyText={t('people.import.noRows')}
        hasMore={canPage && cursor !== undefined}
        loadingMore={loading && visible.length > 0}
        onLoadMore={() => void load(action, cursor)}
      />
    </>
  );
}

/** Summary above the rows: file hash, expiry and the unknown columns the server reported. */
export function PreviewNotes({ preview }: { preview: ImportBatchPreview }) {
  const { t, locale } = useI18n();
  return (
    <>
      {preview.unknownColumns.length > 0 ? (
        <Alert kind="warning">
          {t('people.import.unknownColumns', { columns: preview.unknownColumns.join(', ') })}
        </Alert>
      ) : null}
      {preview.status === 'previewed' ? (
        <p className="field-hint">
          {t('people.import.expires', { time: formatDateTime(locale, preview.expiresAt) })}
        </p>
      ) : null}
    </>
  );
}
