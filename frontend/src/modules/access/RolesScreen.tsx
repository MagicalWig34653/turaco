import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Tabs } from '../../platform/ui/Workspace';
import { accessApi } from './api';
import { RoleTemplates } from './RoleTemplates';
import type { Role, RoleTemplate } from './types';

export function RolesScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const [tab, setTab] = useState<'roles' | 'templates'>(() =>
    new URLSearchParams(window.location.search).get('tab') === 'templates' ? 'templates' : 'roles',
  );
  const roles = useAsync((signal) => accessApi.roles(signal), []);
  const templates = useAsync(
    (signal): Promise<{ items: RoleTemplate[] }> =>
      accessApi.templates(signal).catch(() => ({ items: [] })),
    [],
  );
  const permissions = useAsync((signal) => accessApi.permissions(signal), []);
  const templateByKey = new Map(
    (templates.data?.items ?? []).map((template) => [template.key, template]),
  );

  const columns: Column<Role>[] = [
    {
      key: 'name',
      sortValue: (role) => role.name,
      header: t('roles.col.name'),
      render: (role) => <Link to={`/admin/roles/${encodeURIComponent(role.id)}`}>{role.name}</Link>,
    },
    { key: 'key', header: t('roles.col.key'), render: (role) => <code>{role.key}</code> },
    {
      key: 'builtIn',
      header: t('roles.col.type'),
      render: (role) => {
        const template = role.templateKey ? templateByKey.get(role.templateKey) : undefined;
        const behind =
          template && role.templateVersion !== undefined && template.version > role.templateVersion;
        return (
          <span className="adm-chip-row">
            {role.builtIn ? <Badge>{t('roles.builtIn')}</Badge> : <span>{t('roles.custom')}</span>}
            {role.templateKey ? (
              <Badge tone={behind ? 'warning' : 'info'}>
                {behind
                  ? t('roles.fromTemplate.behind', { key: role.templateKey })
                  : t('roles.fromTemplate', { key: role.templateKey })}
              </Badge>
            ) : null}
          </span>
        );
      },
    },
    {
      key: 'permissions',
      sortValue: (role) => role.permissions.length,
      header: t('roles.col.permissions'),
      className: 'num',
      render: (role) => role.permissions.length,
    },
    {
      key: 'assignments',
      sortValue: (role) => role.activeAssignments,
      header: t('roles.col.assignments'),
      className: 'num',
      render: (role) => role.activeAssignments,
    },
  ];

  return (
    <>
      <PageHeader
        title={t('nav.roles')}
        eyebrow={t('sidebar.section.admin')}
        intro={t('roles.intro')}
        actions={
          can('platform.roles.manage') ? (
            <Link to="/admin/roles/new" className="btn btn-primary">
              {t('roles.create.action')}
            </Link>
          ) : null
        }
      />
      <Tabs
        idPrefix="roles"
        active={tab}
        onChange={(next) => {
          setTab(next as 'roles' | 'templates');
          const url = new URL(window.location.href);
          if (next === 'templates') url.searchParams.set('tab', 'templates');
          else url.searchParams.delete('tab');
          navigate(`${url.pathname}${url.search}`, { replace: true });
        }}
        items={[
          { id: 'roles', label: t('roles.tab.roles') },
          { id: 'templates', label: t('roles.tab.templates') },
        ]}
      />
      {tab === 'roles' ? (
        <div
          role="tabpanel"
          id="roles-panel-roles"
          aria-labelledby="roles-tab-roles"
          className="adm-panel"
        >
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
        </div>
      ) : (
        <div
          role="tabpanel"
          id="roles-panel-templates"
          aria-labelledby="roles-tab-templates"
          className="adm-panel"
        >
          <p className="subtitle">{t('roles.templates.intro')}</p>
          {templates.error ? (
            <ApiErrorAlert error={templates.error} onRetry={templates.reload} />
          ) : null}
          {permissions.error ? (
            <ApiErrorAlert error={permissions.error} onRetry={permissions.reload} />
          ) : null}
          {!templates.loading && (templates.data?.items.length ?? 0) === 0 ? (
            <Alert kind="info">{t('roles.templates.unavailable')}</Alert>
          ) : null}
          {templates.data ? (
            <RoleTemplates
              templates={templates.data.items}
              permissions={permissions.data?.items ?? []}
            />
          ) : null}
        </div>
      )}
    </>
  );
}
