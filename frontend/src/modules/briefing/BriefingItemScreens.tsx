import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { availableActions, isExpired } from './actions';
import { briefingApi } from './api';
import { BriefingForm, type BriefingFormValues } from './BriefingForm';
import { SeverityBadge } from './BriefingScreen';

const listPath = '/briefing';

export function BriefingCreateScreen() {
  const { t } = useI18n();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const create = async (values: BriefingFormValues) => {
    setBusy(true);
    setError(undefined);
    try {
      const item = await briefingApi.create({
        title: values.title,
        body: values.body,
        severity: values.severity,
        validUntil: values.validUntil,
      });
      navigate(`${listPath}/${encodeURIComponent(item.id)}`);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title={t('briefing.create.title')} intro={t('briefing.create.intro')} />
      {error ? <ApiErrorAlert error={error} /> : null}
      <BriefingForm
        submitLabel={t('briefing.create.action')}
        busy={busy}
        onSubmit={(values) => void create(values)}
        onCancel={() => navigate(listPath)}
      />
    </>
  );
}

export function BriefingDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => briefingApi.get(id, signal), [id]);
  const [editing, setEditing] = useState(false);
  const [confirm, setConfirm] = useState<'publish' | 'withdraw' | 'delete' | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const item = loaded.data;
  if (!item) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }

  const run = async (call: () => Promise<unknown>, after?: () => void) => {
    setBusy(true);
    setError(undefined);
    try {
      await call();
      setConfirm(null);
      setEditing(false);
      if (after) after();
      else loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  if (editing) {
    return (
      <>
        <PageHeader title={t('briefing.edit.title')} />
        {error ? <ApiErrorAlert error={error} /> : null}
        <BriefingForm
          initial={item}
          submitLabel={t('action.save')}
          busy={busy}
          onSubmit={(values) =>
            void run(() =>
              briefingApi.update(item.id, {
                expectedVersion: item.version,
                title: values.title,
                body: values.body,
                severity: values.severity,
                ...(values.validUntil === null
                  ? { clearValidUntil: true }
                  : { validUntil: values.validUntil }),
              }),
            )
          }
          onCancel={() => setEditing(false)}
        />
      </>
    );
  }

  const actions = availableActions(item, can);
  const expired = item.status === 'published' && isExpired(item, new Date());
  return (
    <>
      <PageHeader
        title={item.title}
        actions={
          <>
            {actions.includes('edit') ? (
              <Button onClick={() => setEditing(true)}>{t('tasks.action.edit')}</Button>
            ) : null}
            {actions.includes('publish') ? (
              <Button variant="primary" onClick={() => setConfirm('publish')}>
                {t('briefing.action.publish')}
              </Button>
            ) : null}
            {actions.includes('withdraw') ? (
              <Button onClick={() => setConfirm('withdraw')}>
                {t('briefing.action.withdraw')}
              </Button>
            ) : null}
            {actions.includes('delete') ? (
              <Button variant="danger" onClick={() => setConfirm('delete')}>
                {t('recurrence.action.delete')}
              </Button>
            ) : null}
          </>
        }
      />
      <p>
        <Link to={listPath}>{t('briefing.back')}</Link>
      </p>
      {error ? <ApiErrorAlert error={error} /> : null}
      <dl className="facts">
        <dt>{t('briefing.col.severity')}</dt>
        <dd>
          <SeverityBadge severity={item.severity} />
        </dd>
        {can('briefing.manage') ? (
          <>
            <dt>{t('tasks.col.status')}</dt>
            <dd>
              {t(`briefing.status.${item.status}`)}
              {expired ? ` (${t('briefing.expired')})` : ''}
            </dd>
          </>
        ) : null}
        <dt>{t('briefing.col.published')}</dt>
        <dd>{item.publishedAt ? formatDateTime(locale, item.publishedAt) : '–'}</dd>
        <dt>{t('briefing.col.validUntil')}</dt>
        <dd>{item.validUntil ? formatDateTime(locale, item.validUntil) : '–'}</dd>
        {item.withdrawnAt ? (
          <>
            <dt>{t('briefing.fact.withdrawnAt')}</dt>
            <dd>{formatDateTime(locale, item.withdrawnAt)}</dd>
          </>
        ) : null}
      </dl>
      {item.body ? <p className="preline">{item.body}</p> : null}
      {confirm ? (
        <ConfirmDialog
          title={t(`briefing.confirm.${confirm}.title`)}
          message={t(`briefing.confirm.${confirm}.message`, { title: item.title })}
          confirmLabel={
            confirm === 'delete' ? t('recurrence.action.delete') : t(`briefing.action.${confirm}`)
          }
          danger={confirm !== 'publish'}
          busy={busy}
          error={error ? t('error.generic') : undefined}
          onConfirm={() =>
            void run(
              () =>
                confirm === 'publish'
                  ? briefingApi.publish(item.id, item.version)
                  : confirm === 'withdraw'
                    ? briefingApi.withdraw(item.id, item.version)
                    : briefingApi.remove(item.id, item.version),
              confirm === 'delete' ? () => navigate(listPath) : undefined,
            )
          }
          onCancel={() => setConfirm(null)}
        />
      ) : null}
    </>
  );
}
