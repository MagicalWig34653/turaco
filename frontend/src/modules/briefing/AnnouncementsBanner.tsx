import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { briefingApi } from './api';

/**
 * Announcements for every signed-in person (published briefing items with audience "all"). Open major
 * incidents are shown by the IncidentBanner next to it. Renders nothing when there is nothing to say or
 * the briefing is not available to this person.
 */
export function AnnouncementsBanner() {
  const { t } = useI18n();
  const loaded = useAsync(async (signal) => (await briefingApi.announcements(signal)).items, []);
  if (!loaded.data || loaded.data.length === 0) return null;
  return (
    <section aria-label={t('briefing.announcements.title')}>
      {loaded.data.map((item) => (
        <Alert key={item.id} kind={item.severity === 'info' ? 'info' : 'warning'}>
          <strong>{item.title}</strong>
          {item.body ? <p className="preline">{item.body}</p> : null}
        </Alert>
      ))}
    </section>
  );
}
