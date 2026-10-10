import { useI18n } from '../../platform/i18n/I18nProvider';
import { Badge } from '../../platform/ui/Alert';
import { statusTone } from './adminModel';
import type { PersonRow } from './adminTypes';

type KnownStatus = 'active' | 'inactive' | 'departed' | 'external' | 'unknown';
const known: readonly string[] = ['active', 'inactive', 'departed', 'external', 'unknown'];
const knownStatus = (status: string): KnownStatus =>
  known.includes(status) ? (status as KnownStatus) : 'unknown';

export function PersonStatusBadge({ status }: { status: string }) {
  const { t } = useI18n();
  return <Badge tone={statusTone(status)}>{t(`people.status.${knownStatus(status)}`)}</Badge>;
}

/** Where the account comes from, as text, so it never depends on color alone. */
export function AccountBadges({ person }: { person: Pick<PersonRow, 'source' | 'accountKind'> }) {
  const { t } = useI18n();
  return (
    <>
      <Badge
        tone={
          person.source === 'emergency'
            ? 'warning'
            : person.source === 'directory'
              ? 'info'
              : 'neutral'
        }
      >
        {t(`people.source.${person.source}`)}
      </Badge>
      {person.accountKind === 'external' ? (
        <Badge tone="warning">{t('people.kind.external')}</Badge>
      ) : null}
    </>
  );
}
