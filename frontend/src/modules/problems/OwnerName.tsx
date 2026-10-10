import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useSession } from '../../platform/session/SessionProvider';
import { organizationApi } from '../organization/api';

/** Display name of a problem owner; people without directory access see a neutral label. */
export function OwnerName({ ownerId }: { ownerId: string | null }) {
  const { t } = useI18n();
  const { can } = useSession();
  const allowed = can('organization.view');
  const name = useAsync(
    async (signal) => {
      if (!ownerId || !allowed) return null;
      try {
        return (await organizationApi.user(ownerId, signal)).displayName;
      } catch {
        return null;
      }
    },
    [ownerId, allowed],
  );
  if (!ownerId) return <>{t('problems.owner.none')}</>;
  return <>{name.data ?? t('problems.owner.assigned')}</>;
}
