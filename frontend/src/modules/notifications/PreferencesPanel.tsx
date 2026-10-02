import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Checkbox } from '../../platform/ui/Field';
import { notificationsApi } from './api';
import { categoryLabel } from './text';

/** Per-category email opt-out. Only the choice is stored; whether email is configured is not shown. */
export function PreferencesPanel() {
  const { t } = useI18n();
  const prefs = useAsync((signal) => notificationsApi.preferences(signal), []);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [saving, setSaving] = useState<string | null>(null);

  const toggle = async (category: string, enabled: boolean) => {
    setSaving(category);
    setError(undefined);
    try {
      await notificationsApi.setEmailPreference(category, enabled);
      prefs.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setSaving(null);
    }
  };

  if (prefs.error) return <ApiErrorAlert error={prefs.error} onRetry={prefs.reload} />;
  return (
    <section aria-labelledby="notification-preferences">
      <h2 id="notification-preferences">{t('notifications.prefs.title')}</h2>
      <p className="field-hint">{t('notifications.prefs.hint')}</p>
      {error ? <ApiErrorAlert error={error} /> : null}
      {(prefs.data?.items ?? []).map((pref) => (
        <Checkbox
          key={pref.category}
          label={categoryLabel(t, pref.category)}
          checked={pref.enabled}
          disabled={saving !== null}
          onChange={(event) => void toggle(pref.category, event.target.checked)}
        />
      ))}
    </section>
  );
}
