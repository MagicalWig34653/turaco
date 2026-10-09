import { useI18n } from '../../platform/i18n/I18nProvider';
import { Button } from '../../platform/ui/Button';
import { sourceLabelKey } from './feedModel';

/** A source of My Work failed: say which, and that its items are missing rather than absent. */
export function SourceNotice({
  sources,
  onRetry,
}: {
  sources: readonly string[];
  onRetry: () => void;
}) {
  const { t } = useI18n();
  const names = sources.map((source) => t(sourceLabelKey(source))).join(', ');
  return (
    <p role="alert" className="workspace-source-notice">
      {t('myWork.partial', { sources: names })}{' '}
      <Button onClick={onRetry}>{t('action.retry')}</Button>
    </p>
  );
}
