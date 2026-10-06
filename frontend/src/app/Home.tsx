import { useEffect } from 'react';
import { useI18n } from '../platform/i18n/I18nProvider';
import { Link, navigate } from '../platform/router/Router';
import { useSession } from '../platform/session/SessionProvider';
import { PageHeader } from '../platform/ui/PageHeader';
import { Card } from '../platform/ui/Workspace';

export function Home() {
  const { t } = useI18n();
  const { can } = useSession();
  const destination =
    can('tasks.view') || can('tasks.manage') || can('tasks.work') ? '/my-work' : '/requests';
  useEffect(() => {
    navigate(destination, { replace: true });
  }, [destination]);
  return (
    <>
      <PageHeader title={t('app.name')} intro={t('home.redirect')} />
      <Card>
        <Link to={destination}>{t('home.continue')}</Link>
      </Card>
    </>
  );
}
