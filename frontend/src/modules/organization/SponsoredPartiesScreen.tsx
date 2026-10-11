import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ExtendAccessDialog, type ExtendTarget } from './AccessDialogs';
import { externalPartiesApi, type SponsoredAccount, type SponsoredParty } from './externalApi';

/**
 * The sponsor's own page: the External Parties a person sponsors and their accounts, with the one operation a
 * sponsor has, extending access. It needs no permission; the server returns only the parties of the signed-in
 * person, and ending access stays with the administrators.
 */
export function SponsoredPartiesScreen() {
  const { t } = useI18n();
  const sponsored = useAsync((signal) => externalPartiesApi.sponsored(signal), []);
  const [extending, setExtending] = useState<ExtendTarget | null>(null);
  if (sponsored.error) return <ApiErrorAlert error={sponsored.error} onRetry={sponsored.reload} />;
  if (!sponsored.data) return <p role="status">{t('state.loading')}</p>;
  const parties = sponsored.data.items;
  return (
    <>
      <PageHeader title={t('sponsored.title')} intro={t('sponsored.intro')} />
      {parties.length === 0 ? <p>{t('sponsored.empty')}</p> : null}
      {parties.map((party) => (
        <PartySection
          key={party.id}
          party={party}
          onExtend={(account) =>
            setExtending({
              id: account.userId,
              displayName: account.displayName,
              status: account.status,
              version: account.version,
              accessExpiresAt: account.accessExpiresAt,
            })
          }
        />
      ))}
      {extending ? (
        <ExtendAccessDialog
          person={extending}
          onChanged={() => {
            setExtending(null);
            sponsored.reload();
          }}
          onClose={() => setExtending(null)}
        />
      ) : null}
    </>
  );
}

function PartySection({
  party,
  onExtend,
}: {
  party: SponsoredParty;
  onExtend: (account: SponsoredAccount) => void;
}) {
  const { t, locale } = useI18n();
  const columns: Column<SponsoredAccount>[] = [
    {
      key: 'name',
      sortValue: (account) => account.displayName,
      header: t('sponsored.col.name'),
      render: (account) => account.displayName,
    },
    {
      key: 'status',
      header: t('sponsored.col.status'),
      render: (account) => (
        <Badge tone={account.status === 'active' ? 'success' : 'warning'}>
          {t(account.status === 'active' ? 'external.status.active' : 'external.status.inactive')}
        </Badge>
      ),
    },
    {
      key: 'ends',
      sortValue: (account) => account.accessExpiresAt,
      header: t('sponsored.col.ends'),
      render: (account) => formatDateTime(locale, account.accessExpiresAt),
    },
    {
      key: 'actions',
      header: t('sponsored.col.actions'),
      render: (account) =>
        party.active ? (
          <Button onClick={() => onExtend(account)}>{t('sponsored.extend')}</Button>
        ) : (
          <span>{t('sponsored.partyInactive')}</span>
        ),
    },
  ];
  return (
    <section aria-label={party.name}>
      <h2>
        {party.name} <small>{t(`external.kind.${party.kind}`)}</small>
      </h2>
      <DataTable
        caption={party.name}
        columns={columns}
        rows={party.accounts}
        rowKey={(account) => account.userId}
        emptyText={t('sponsored.noAccounts')}
      />
    </section>
  );
}
