import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { useContextMenu, type MenuItem } from '../../platform/ui/ContextMenu';
import { PageHeader } from '../../platform/ui/PageHeader';
import { Tabs } from '../../platform/ui/Workspace';
import { EffectivePermissionsPanel } from '../access/EffectivePermissionsPanel';
import { peopleAdminApi } from './adminApi';
import { lifecycleActions, type LifecycleAction } from './adminModel';
import { AccountBadges, PersonStatusBadge } from './PersonBadges';
import { useOrgLookups } from './orgLookups';
import { accessActions, type AccessAction } from './importModel';
import { useAccessDialogs } from './AccessDialogs';
import { EntraSection } from './EntraSection';
import { EditProfileDialog, ProfileCard, SignInCard } from './UserProfile';
import { UserRolesTab, UserTeamsTab } from './UserTabs';
import { useLifecycle } from './UserLifecycle';

type TabId = 'profile' | 'teams' | 'roles' | 'effective';

const actionLabel: Record<LifecycleAction, MessageKey> = {
  sendInvitation: 'lifecycle.action.sendInvitation',
  resetPassword: 'lifecycle.action.resetPassword',
  deactivate: 'lifecycle.action.deactivate',
  reactivate: 'lifecycle.action.reactivate',
  markDeparted: 'lifecycle.action.markDeparted',
};

const accessLabel: Record<AccessAction, MessageKey> = {
  linkDirectory: 'people.link.action',
  extendAccess: 'people.extend.action',
};

export function UserDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can } = useSession();
  const menu = useContextMenu();
  const lookups = useOrgLookups();
  const person = useAsync((signal) => peopleAdminApi.user(id, signal), [id]);
  const canManage = can('organization.users.manage');
  const canRoles = can('platform.roles.view');
  const canEffective = canRoles && can('organization.users.view_details');
  const [tab, setTab] = useState<TabId>(() => {
    const requested = new URLSearchParams(window.location.search).get('tab');
    return requested === 'teams' || requested === 'roles' || requested === 'effective'
      ? requested
      : 'profile';
  });
  const [editing, setEditing] = useState(false);
  const lifecycle = useLifecycle(() => person.reload());
  const access = useAccessDialogs(() => person.reload());

  const select = (next: TabId) => {
    setTab(next);
    const url = new URL(window.location.href);
    if (next === 'profile') url.searchParams.delete('tab');
    else url.searchParams.set('tab', next);
    navigate(`${url.pathname}${url.search}`, { replace: true });
  };

  if (person.loading && !person.data) {
    return (
      <>
        <PageHeader title={t('people.detail.title')} eyebrow={t('sidebar.section.admin')} />
        <p role="status">{t('state.loading')}</p>
      </>
    );
  }
  if (person.error) {
    return (
      <>
        <PageHeader
          title={t('people.detail.title')}
          eyebrow={t('sidebar.section.admin')}
          actions={
            <Link to="/admin/users" className="btn btn-secondary">
              {t('people.back')}
            </Link>
          }
        />
        <ApiErrorAlert error={person.error} onRetry={person.reload} />
      </>
    );
  }
  const data = person.data;
  if (!data) return null;

  const actions = lifecycleActions(data, canManage);
  const items: MenuItem[] = actions.map((action) => ({
    id: action,
    label: t(actionLabel[action]),
    danger: action === 'deactivate' || action === 'markDeparted',
    onSelect: () => lifecycle.start(action, data),
  }));
  items.push(
    ...accessActions(data, can).map((action): MenuItem => ({
      id: action,
      label: t(accessLabel[action]),
      danger: action === 'linkDirectory',
      onSelect: () => access.start(action, data),
    })),
  );
  const tabs: { id: TabId; label: string }[] = [
    { id: 'profile', label: t('people.tab.profile') },
    { id: 'teams', label: t('people.tab.teams') },
    ...(canRoles ? [{ id: 'roles' as const, label: t('people.tab.roles') }] : []),
    ...(canEffective ? [{ id: 'effective' as const, label: t('people.tab.effective') }] : []),
  ];
  const activeTab = tabs.some((entry) => entry.id === tab) ? tab : 'profile';
  const emergency = data.source === 'emergency';
  const hasDirectoryFields = data.fields.some((field) => field.owner === 'directory');

  return (
    <>
      <PageHeader
        title={data.displayName}
        eyebrow={t('sidebar.section.admin')}
        intro={data.primaryEmail ?? undefined}
        actions={
          <>
            {canManage && !emergency ? (
              <Button variant="primary" onClick={() => setEditing(true)}>
                {t('people.edit.action')}
              </Button>
            ) : null}
            {items.length > 0 ? (
              <Button
                aria-haspopup="menu"
                onClick={(event) =>
                  menu.openAtElement(items, event.currentTarget, t('people.actions'))
                }
              >
                {t('people.actions')} ▾
              </Button>
            ) : null}
            <Link to="/admin/users" className="btn btn-secondary">
              {t('people.back')}
            </Link>
          </>
        }
      />
      <div className="adm-chip-row" aria-label={t('people.col.status')}>
        <PersonStatusBadge status={data.status} />
        <AccountBadges person={data} />
      </div>
      {emergency ? <Alert kind="info">{t('people.emergency.hint')}</Alert> : null}
      {hasDirectoryFields && !emergency ? (
        <Alert kind="info">{t('people.directory.hint')}</Alert>
      ) : null}
      <Tabs
        idPrefix="user"
        items={tabs}
        active={activeTab}
        onChange={(next) => select(next as TabId)}
      />
      <div
        role="tabpanel"
        id={`user-panel-${activeTab}`}
        aria-labelledby={`user-tab-${activeTab}`}
        className="adm-panel"
      >
        {activeTab === 'profile' ? (
          <div className="adm-two-col">
            <ProfileCard person={data} lookups={lookups} />
            <div>
              <SignInCard person={data} />
              <EntraSection person={data} onChanged={person.reload} />
            </div>
          </div>
        ) : null}
        {activeTab === 'teams' ? <UserTeamsTab person={data} /> : null}
        {activeTab === 'roles' ? <UserRolesTab person={data} /> : null}
        {activeTab === 'effective' ? <EffectivePermissionsPanel userId={data.id} /> : null}
      </div>
      {menu.menu}
      {lifecycle.dialog}
      {access.dialog}
      {editing ? (
        <EditProfileDialog
          person={data}
          lookups={lookups}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            person.reload();
          }}
        />
      ) : null}
    </>
  );
}
