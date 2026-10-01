import { endpoints } from '../../platform/api/endpoints';
import type { Role } from '../../platform/api/types';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';

export function RolesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const roles = useAsync((signal) => endpoints.roles(signal), []);

  const columns: Column<Role>[] = [
    {
      key: 'name',
      header: t('roles.col.name'),
      render: (role) => <Link to={`/admin/roles/${encodeURIComponent(role.id)}`}>{role.name}</Link>,
    },
    { key: 'key', header: t('roles.col.key'), render: (role) => <code>{role.key}</code> },
    {
      key: 'builtIn',
      header: t('roles.col.type'),
      render: (role) => (role.builtIn ? <Badge>{t('roles.builtIn')}</Badge> : t('roles.custom')),
    },
    {
      key: 'permissions',
      header: t('roles.col.permissions'),
      className: 'num',
      render: (role) => role.permissions.length,
    },
    {
      key: 'assignments',
      header: t('roles.col.assignments'),
      className: 'num',
      render: (role) => role.activeAssignments,
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.roles')}
        intro={t('roles.intro')}
        actions={
          can('platform.roles.manage') ? (
            <Link to="/admin/roles/new" className="btn btn-primary">
              {t('roles.create.action')}
            </Link>
          ) : null
        }
      />
      <DataTable
        caption={t('nav.roles')}
        columns={columns}
        rows={roles.data?.items ?? []}
        rowKey={(role) => role.id}
        loading={roles.loading}
        error={roles.error}
        onRetry={roles.reload}
        emptyText={t('roles.empty')}
      />
    </>
  );
}
