import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { approvalsApi } from './api';
import type { Approval } from './types';

export function ApprovalStatusBadge({ status }: { status: Approval['status'] }) {
  const { t } = useI18n();
  const tone = status === 'approved' ? 'success' : status === 'rejected' ? 'danger' : 'neutral';
  return <Badge tone={tone}>{t(`approvals.status.${status}`)}</Badge>;
}

export function ApprovalsScreen() {
  const { t, locale } = useI18n();
  const [tab, setTab] = useState<'pending' | 'decided'>('pending');
  const list = usePagedList((cursor, signal) => approvalsApi.list(tab, cursor, signal), [tab]);
  const columns: Column<Approval>[] = [
    {
      key: 'subject',
      header: t('approvals.col.subject'),
      render: (a) => <Link to={`/approvals/${encodeURIComponent(a.id)}`}>{a.subjectLabel}</Link>,
    },
    { key: 'step', header: t('approvals.col.step'), render: (a) => a.stepIndex + 1 },
    {
      key: 'status',
      header: t('approvals.col.status'),
      render: (a) => <ApprovalStatusBadge status={a.status} />,
    },
    {
      key: 'created',
      header: t('approvals.col.created'),
      render: (a) => formatDateTime(locale, a.createdAt),
    },
  ];
  return (
    <>
      <PageHeader title={t('nav.approvals')} intro={t('approvals.intro')} />
      <div className="tabs" role="group" aria-label={t('nav.approvals')}>
        {(['pending', 'decided'] as const).map((value) => (
          <Button
            key={value}
            variant={tab === value ? 'primary' : 'secondary'}
            aria-pressed={tab === value}
            onClick={() => setTab(value)}
          >
            {t(`approvals.tab.${value}`)}
          </Button>
        ))}
      </div>
      <DataTable
        caption={t('nav.approvals')}
        columns={columns}
        rows={list.items}
        rowKey={(a) => a.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t(`approvals.empty.${tab}`)}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
