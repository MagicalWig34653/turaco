import { useState } from 'react';
import { usePagedList } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select } from '../../platform/ui/Field';
import { SCOPABLE_HOLDER_PERMISSIONS, locationPathText } from '../access/accessModel';
import { accessApi } from '../access/api';
import type { Holder } from '../access/types';

/** "Who has rights here": holders whose scope covers one Location, for a chosen scopable permission. */
export function LocationHolders({ locationId }: { locationId: string }) {
  const { t } = useI18n();
  const [permission, setPermission] = useState<string>(SCOPABLE_HOLDER_PERMISSIONS[0]);
  const list = usePagedList(
    (cursor, signal) => accessApi.holders(permission, cursor, signal, locationId),
    [permission, locationId],
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
      key: 'scope',
      header: t('assignments.col.scope'),
      render: (holder) =>
        holder.scope === 'location'
          ? t('assignments.scope.at', {
              name:
                locationPathText(holder.scopeLocationPath, holder.scopeLocationName) ||
                t('assignments.scope.unknown'),
            })
          : t('assignments.scope.global'),
    },
  ];
  return (
    <section className="adm-location-holders" aria-label={t('locations.holders.title')}>
      <h3>{t('locations.holders.title')}</h3>
      <p className="field-hint">{t('locations.holders.intro')}</p>
      <Select
        label={t('holders.permission')}
        value={permission}
        onChange={(event) => setPermission(event.target.value)}
        options={SCOPABLE_HOLDER_PERMISSIONS.map((name) => ({ value: name, label: name }))}
      />
      <DataTable
        caption={t('locations.holders.title')}
        columns={columns}
        rows={list.items}
        rowKey={(holder) => holder.assignmentId}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('locations.holders.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
    </section>
  );
}
