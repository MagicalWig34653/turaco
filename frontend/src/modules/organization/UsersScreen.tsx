import { useState } from 'react';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Button } from '../../platform/ui/Button';
import type { MenuItem } from '../../platform/ui/ContextMenu';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { Avatar } from '../../platform/ui/Workspace';
import { QueryWorkbench } from '../../platform/ui/query/QueryWorkbench';
import { useQueryList } from '../../platform/ui/query/useQueryList';
import { lifecycleActions, type LifecycleAction } from './adminModel';
import type { PersonRow } from './adminTypes';
import { AccountBadges, PersonStatusBadge } from './PersonBadges';
import { useOrgLookups } from './orgLookups';
import { peoplePresets, presetState, type PeoplePreset } from './peoplePresets';
import { useLifecycle } from './UserLifecycle';

const actionLabel: Record<LifecycleAction, MessageKey> = {
  sendInvitation: 'lifecycle.action.sendInvitation',
  resetPassword: 'lifecycle.action.resetPassword',
  deactivate: 'lifecycle.action.deactivate',
  reactivate: 'lifecycle.action.reactivate',
  markDeparted: 'lifecycle.action.markDeparted',
};

export function UsersScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const canManage = can('organization.users.manage');
  const canDetails = can('organization.users.view_details');
  const query = useQueryList<PersonRow>('users');
  const lookups = useOrgLookups();
  const [visibleKeys, setVisibleKeys] = useState<string[]>();
  const lifecycle = useLifecycle(() => query.list.reload());
  const { list } = query;

  const columns: Column<PersonRow>[] = [
    {
      key: 'name',
      sortField: 'display_name',
      header: t('people.col.name'),
      render: (person) => (
        <span className="adm-person-cell">
          <Avatar name={person.displayName} />
          <span>
            <Link to={`/admin/users/${encodeURIComponent(person.id)}`}>{person.displayName}</Link>
            {person.primaryEmail ? <span className="adm-sub">{person.primaryEmail}</span> : null}
          </span>
        </span>
      ),
    },
    {
      key: 'status',
      sortField: 'status',
      header: t('people.col.status'),
      render: (person) => <PersonStatusBadge status={person.status} />,
    },
    {
      key: 'account',
      header: t('people.col.account'),
      render: (person) => <AccountBadges person={person} />,
    },
    {
      key: 'department',
      header: t('people.col.department'),
      render: (person) =>
        person.departmentId ? (lookups.departmentName(person.departmentId) ?? '–') : '–',
    },
    ...(canDetails
      ? [
          {
            key: 'location',
            header: t('people.col.location'),
            render: (person: PersonRow) =>
              person.primaryLocationId
                ? (lookups.locationName(person.primaryLocationId) ?? '–')
                : '–',
          } satisfies Column<PersonRow>,
        ]
      : []),
    {
      key: 'updated',
      sortField: 'updated_at',
      header: t('people.col.updated'),
      render: (person) => <TableDate value={person.updatedAt} />,
    },
  ];

  const rowActions = (person: PersonRow): MenuItem[] => [
    {
      id: 'open',
      label: t('contextMenu.open'),
      onSelect: () => navigate(`/admin/users/${encodeURIComponent(person.id)}`),
    },
    ...(person.source === 'emergency'
      ? [
          {
            id: 'emergency',
            label: t('people.emergency.noActions'),
            disabledReason: t('people.emergency.hint'),
            onSelect: () => undefined,
          } satisfies MenuItem,
        ]
      : []),
    ...lifecycleActions(person, canManage).map((action): MenuItem => ({
      id: action,
      label: t(actionLabel[action]),
      danger: action === 'deactivate' || action === 'markDeparted',
      onSelect: () => lifecycle.start(action, person),
    })),
  ];

  return (
    <>
      <PageHeader
        title={t('people.title')}
        eyebrow={t('sidebar.section.admin')}
        intro={t('people.intro')}
        actions={
          canManage ? (
            <Link to="/admin/users/new" className="btn btn-primary">
              {t('people.create.action')}
            </Link>
          ) : null
        }
      />
      <div className="adm-presets" role="group" aria-label={t('people.presets.label')}>
        {peoplePresets.map((preset: PeoplePreset) => (
          <Button key={preset} onClick={() => query.setState(presetState(preset))}>
            {t(`people.preset.${preset}`)}
          </Button>
        ))}
      </div>
      <QueryWorkbench
        query={query}
        columns={columns}
        listKey="users"
        onColumnsChange={setVisibleKeys}
      />
      <DataTable
        caption={t('people.title')}
        columns={
          visibleKeys
            ? visibleKeys.flatMap((key) => columns.filter((column) => column.key === key))
            : columns
        }
        serverSort={query.state.sort}
        onSortChange={(sort) => query.setState({ ...query.state, sort })}
        totalCount={query.countCapped ? undefined : query.count}
        rows={list.items}
        rowKey={(person) => person.id}
        rowActions={rowActions}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('people.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {lifecycle.dialog}
    </>
  );
}
