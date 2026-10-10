import { useState } from 'react';
import { useAsync, usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { useFilterQuery } from '../../platform/ui/useFilterQuery';
import { accessApi } from './api';
import { RiskBadge } from './PermissionPicker';
import { ExpiryCell } from './RoleAssignmentsScreen';
import type { Holder } from './types';

/** "Who can do this?" for one permission: direct holders and holders through Directory Groups. */
export function HoldersScreen() {
  const { t } = useI18n();
  const [permission, setPermission] = useState(
    () => new URLSearchParams(window.location.search).get('permission') ?? '',
  );
  useFilterQuery({ permission });
  const catalog = useAsync((signal) => accessApi.permissions(signal), []);
  const known = (catalog.data?.items ?? []).find((entry) => entry.name === permission);
  const list = usePagedList(
    async (cursor, signal) =>
      known ? accessApi.holders(permission, cursor, signal) : { items: [] as Holder[] },
    [permission, known?.name],
  );
  const columns: Column<Holder>[] = [
    {
      key: 'subject',
      header: t('holders.col.subject'),
      render: (holder) =>
        holder.subjectType === 'user' ? (
          <Link to={`/admin/users/${encodeURIComponent(holder.subjectId)}`}>
            {holder.subjectDisplayName}
          </Link>
        ) : (
          <span>{holder.subjectDisplayName}</span>
        ),
    },
    {
      key: 'via',
      header: t('holders.col.via'),
      render: (holder) => (
        <Badge tone={holder.subjectType === 'user' ? 'neutral' : 'info'}>
          {t(holder.subjectType === 'user' ? 'holders.via.direct' : 'holders.via.group')}
        </Badge>
      ),
    },
    {
      key: 'role',
      header: t('holders.col.role'),
      render: (holder) => (
        <Link to={`/admin/roles/${encodeURIComponent(holder.roleId)}`}>{holder.roleName}</Link>
      ),
    },
    {
      key: 'expires',
      header: t('assignments.col.expires'),
      render: (holder) => <ExpiryCell expiresAt={holder.expiresAt} />,
    },
  ];
  return (
    <>
      <PageHeader
        title={t('holders.title')}
        eyebrow={t('sidebar.section.admin')}
        intro={t('holders.intro')}
      />
      <div className="form">
        <TextField
          label={t('holders.permission')}
          hint={t('holders.permission.hint')}
          list="holders-permissions"
          value={permission}
          maxLength={100}
          autoComplete="off"
          onChange={(event) => setPermission(event.target.value.trim())}
        />
        <datalist id="holders-permissions">
          {(catalog.data?.items ?? []).map((entry) => (
            <option key={entry.name} value={entry.name} />
          ))}
        </datalist>
        {known ? (
          <p className="field-hint">
            <RiskBadge risk={known.risk} /> {known.description}
          </p>
        ) : permission !== '' && catalog.data ? (
          <p className="field-hint">{t('holders.permission.unknown')}</p>
        ) : null}
      </div>
      <DataTable
        caption={t('holders.title')}
        columns={columns}
        rows={list.items}
        rowKey={(holder) => holder.assignmentId}
        loading={list.loading && known !== undefined}
        error={list.error}
        onRetry={list.reload}
        emptyText={known ? t('holders.empty') : t('holders.choose')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </>
  );
}
