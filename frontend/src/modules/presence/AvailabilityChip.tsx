import { useEffect } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useSession } from '../../platform/session/SessionProvider';
import { StatusBadge } from '../../platform/ui/Workspace';
import { formatDateTime } from '../../platform/format/format';
import { presenceApi } from './api';
import { availabilityTone } from './model';
import { usePresence } from './PresenceProvider';
import type { Availability } from './types';
const explanations: Record<string, MessageKey> = {
  unavailable_entry: 'presence.explain.unavailable_entry',
  work_location: 'presence.explain.work_location',
  remote_when_onsite_needed: 'presence.explain.remote_when_onsite_needed',
  travelling: 'presence.explain.travelling',
  other_location: 'presence.explain.other_location',
  no_entry: 'presence.explain.no_entry',
  source_stale: 'presence.explain.source_stale',
  out_of_scope: 'presence.explain.out_of_scope',
};
export function useAvailability(userIds: string[], enabled = true) {
  const { status, can } = usePresence();
  const { session } = useSession();
  const ids = [...new Set(userIds)]
    .filter(
      (id) =>
        enabled &&
        status?.enabled &&
        (id === session?.userId
          ? can('presence.manage_own') || can('presence.view_availability')
          : can('presence.view_availability')),
    )
    .sort()
    .join(',');
  const loaded = useAsync(
    async (signal) => ({
      ids,
      items: ids ? (await presenceApi.availability(ids, signal)).items : [],
    }),
    [ids],
  );
  useEffect(() => {
    if (!ids) return;
    const timer = window.setInterval(loaded.reload, 60_000);
    return () => window.clearInterval(timer);
  }, [ids, loaded.reload]);
  return {
    visible: !!ids,
    visibleUserIds: ids ? ids.split(',') : [],
    items: loaded.data?.ids === ids && !loaded.error && !loaded.loading ? loaded.data.items : [],
    loading: loaded.loading,
    error: loaded.error,
  };
}
/** Operational hint only: never gates or changes an assignment. */
export function AvailabilityChip({
  availability,
  loading = false,
}: {
  availability?: Availability | undefined;
  loading?: boolean;
}) {
  const { t, locale } = useI18n();
  const { status } = usePresence();
  if (!status?.enabled) return null;
  const value = availability?.value ?? 'unknown';
  return (
    <span className="presence-availability">
      <StatusBadge tone={availabilityTone(value)}>{t(`presence.value.${value}`)}</StatusBadge>
      <small>
        {loading
          ? t('state.loading')
          : t(explanations[availability?.explanation ?? ''] ?? 'presence.explain.unknown')}
      </small>
      {availability?.until ? (
        <small>
          {t('presence.until')}{' '}
          <time dateTime={availability.until}>{formatDateTime(locale, availability.until)}</time>
        </small>
      ) : null}
      {availability?.sources?.length ? (
        availability.sources.map((source, index) => (
          <small key={index}>
            {source.source === 'manual' ? t('presence.manual') : source.source} ·{' '}
            {t(`presence.freshness.${source.freshness}`)}
            {source.observedAt ? (
              <>
                {' '}
                ·{' '}
                <time dateTime={source.observedAt}>
                  {formatDateTime(locale, source.observedAt)}
                </time>
              </>
            ) : null}
          </small>
        ))
      ) : (
        <small>{t('presence.noFreshness')}</small>
      )}
    </span>
  );
}
export function UserAvailability({ userId }: { userId: string }) {
  const loaded = useAvailability([userId]);
  return loaded.visible ? (
    <AvailabilityChip
      availability={loaded.items.find((item) => item.userId === userId)}
      loading={loaded.loading}
    />
  ) : null;
}
