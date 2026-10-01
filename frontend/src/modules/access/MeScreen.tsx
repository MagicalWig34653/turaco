import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime, groupPermissions } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { PageHeader } from '../../platform/ui/PageHeader';
import { organizationApi } from '../organization/api';

const methodKeys: Record<string, MessageKey> = {
  password: 'auth.method.password',
  kerberos: 'auth.method.kerberos',
  emergency: 'auth.method.emergency',
};

export function MeScreen() {
  const { t, locale } = useI18n();
  const { session, can } = useSession();
  const canViewUser = can('organization.view');
  const userId = session?.userId ?? '';
  const user = useAsync(
    (signal) => (canViewUser ? organizationApi.user(userId, signal) : Promise.resolve(undefined)),
    [canViewUser, userId],
  );
  if (!session) return null;

  const methodKey = methodKeys[session.authMethod];
  const groups = groupPermissions(session.permissions, (name) => name);

  return (
    <>
      <PageHeader title={t('nav.me')} intro={t('me.intro')} />
      {session.authMethod === 'emergency' ? (
        <Alert kind="warning">{t('me.emergency')}</Alert>
      ) : null}
      <section className="card-plain" aria-labelledby="me-account">
        <h2 id="me-account">{t('me.account')}</h2>
        <dl className="facts">
          <dt>{t('me.displayName')}</dt>
          <dd>{user.data?.displayName ?? '–'}</dd>
          {user.data?.primaryEmail ? (
            <>
              <dt>{t('me.email')}</dt>
              <dd>{user.data.primaryEmail}</dd>
            </>
          ) : null}
          <dt>{t('me.userId')}</dt>
          <dd>
            <code>{session.userId}</code>
          </dd>
          <dt>{t('me.authMethod')}</dt>
          <dd>{methodKey ? t(methodKey) : session.authMethod}</dd>
          <dt>{t('me.expiresAt')}</dt>
          <dd>{formatDateTime(locale, session.expiresAt)}</dd>
        </dl>
      </section>
      <section className="card-plain" aria-labelledby="me-permissions">
        <h2 id="me-permissions">{t('me.permissions')}</h2>
        <p className="subtitle">
          {t('me.permissions.hint', { count: session.permissions.length })}
        </p>
        {groups.length === 0 ? <p className="empty">{t('me.permissions.none')}</p> : null}
        {groups.map((group) => (
          <div key={group.prefix} className="permission-group">
            <h3>{group.prefix}</h3>
            <ul className="chips">
              {group.items.map((name) => (
                <li key={name}>
                  <code>{name}</code>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </section>
    </>
  );
}
