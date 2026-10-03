import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { PageHeader } from '../../platform/ui/PageHeader';
import { knowledgeApi } from './api';

export function ArticleDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const loaded = useAsync((signal) => knowledgeApi.get(id, signal), [id]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const article = loaded.data;
  if (!article) {
    return (
      <p className="loading" role="status">
        {t('state.loading')}
      </p>
    );
  }
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      loaded.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };
  const manage = can('knowledge.manage');
  return (
    <>
      <PageHeader
        title={article.title}
        actions={
          manage ? (
            <>
              {article.status !== 'retired' ? (
                <Link
                  to={`/knowledge/${encodeURIComponent(article.id)}/edit`}
                  className="btn btn-secondary"
                >
                  {t('knowledge.edit')}
                </Link>
              ) : null}
              {article.status !== 'published' ? (
                <Button
                  variant="primary"
                  busy={busy}
                  onClick={() => void run(() => knowledgeApi.publish(article.id, article.version))}
                >
                  {t(article.status === 'retired' ? 'knowledge.republish' : 'knowledge.publish')}
                </Button>
              ) : (
                <Button
                  busy={busy}
                  onClick={() => void run(() => knowledgeApi.retire(article.id, article.version))}
                >
                  {t('knowledge.retire')}
                </Button>
              )}
            </>
          ) : null
        }
      />
      <p>
        <Link to="/knowledge">{t('knowledge.back')}</Link>
      </p>
      {error ? <ApiErrorAlert error={error} /> : null}
      <p>
        {article.reference} ·{' '}
        <Badge tone={article.status === 'published' ? 'success' : 'neutral'}>
          {t(`knowledge.status.${article.status}`)}
        </Badge>{' '}
        {manage || can('knowledge.view') ? (
          <>· {t(`knowledge.audience.${article.audience}`)} </>
        ) : null}
        · {formatDateTime(locale, article.updatedAt)}
      </p>
      {article.summary ? <p className="subtitle">{article.summary}</p> : null}
      <p className="preline">{article.body}</p>
    </>
  );
}
