import { useI18n } from '../platform/i18n/I18nProvider';
import { Link } from '../platform/router/Router';
import { PageHeader } from '../platform/ui/PageHeader';

export function NotFoundView() {
  const { t } = useI18n();
  return (
    <>
      <PageHeader title={t('notFound.title')} intro={t('notFound.body')} />
      <Link to="/">{t('notFound.home')}</Link>
    </>
  );
}

export function ForbiddenView() {
  const { t } = useI18n();
  return (
    <>
      <PageHeader title={t('forbidden.title')} intro={t('forbidden.body')} />
      <Link to="/">{t('notFound.home')}</Link>
    </>
  );
}
