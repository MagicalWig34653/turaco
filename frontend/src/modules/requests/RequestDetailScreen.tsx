import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { PageHeader } from '../../platform/ui/PageHeader';
import { requestsApi } from './api';
import { RequestDetail } from './RequestDetail';

export function RequestDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const loaded = useAsync((signal) => requestsApi.get(id, signal), [id]);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const request = loaded.data;
  if (!request) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  return (
    <>
      <PageHeader title={`${request.reference} · ${request.catalogItemTitle}`} />
      <p>
        <Link to="/requests">{t('requests.back')}</Link>
      </p>
      <RequestDetail request={request} onChanged={loaded.reload} />
    </>
  );
}
