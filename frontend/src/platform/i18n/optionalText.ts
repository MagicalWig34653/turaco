import { useI18n } from './I18nProvider';
import type { MessageKey, MessageParams } from './i18n';

/**
 * Looks up a message whose key comes from data (a role template, a reason code, a permission module).
 * When the catalog has no entry the given fallback is shown instead of the key.
 */
export function useOptionalText(): (
  key: string,
  fallback: string,
  params?: MessageParams,
) => string {
  const { t } = useI18n();
  return (key, fallback, params) => {
    const text = t(key as MessageKey, params);
    return text === key ? fallback : text;
  };
}
