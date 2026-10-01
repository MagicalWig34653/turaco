import { errorMessageKey } from '../api/errorMessages';
import type { ApiError } from '../api/client';
import { useI18n } from '../i18n/I18nProvider';
import { Alert } from './Alert';
import { Button } from './Button';

export function ApiErrorAlert({
  error,
  onRetry,
}: {
  error: ApiError;
  onRetry?: (() => void) | undefined;
}) {
  const { t } = useI18n();
  return (
    <Alert kind="error">
      <p>{t(errorMessageKey(error))}</p>
      {error.requestId ? (
        <p className="alert-meta">{t('error.reference', { id: error.requestId })}</p>
      ) : null}
      {onRetry ? (
        <Button onClick={onRetry} variant="secondary">
          {t('action.retry')}
        </Button>
      ) : null}
    </Alert>
  );
}
