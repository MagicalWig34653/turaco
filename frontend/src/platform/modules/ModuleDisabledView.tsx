import { useI18n } from '../i18n/I18nProvider';
export function ModuleDisabledView() {
  const { t } = useI18n();
  return (
    <section role="status">
      <h1>{t('modules.disabledTitle')}</h1>
      <p>{t('modules.disabledInfo')}</p>
    </section>
  );
}
