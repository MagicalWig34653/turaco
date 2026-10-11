import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { formatDateTime } from '../../platform/format/format';
import { Link } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import {
  externalPartiesApi,
  partyKinds,
  type ExternalPartyDetail,
  type PartyAccount,
  type PartyKind,
} from './externalApi';
import { PersonPicker, type PickedPerson } from './PersonPicker';
import { PersonStatusBadge } from './PersonBadges';

/** One External Party: its data, its sponsor and the external accounts that belong to it. */
export function ExternalPartyDetailScreen({ id }: { id: string }) {
  const { t, locale } = useI18n();
  const loaded = useAsync((signal) => externalPartiesApi.get(id, signal), [id]);
  const [editing, setEditing] = useState(false);
  const [toggling, setToggling] = useState(false);
  if (loaded.error) return <ApiErrorAlert error={loaded.error} onRetry={loaded.reload} />;
  const party = loaded.data;
  if (!party) return <p role="status">{t('state.loading')}</p>;
  const columns: Column<PartyAccount>[] = [
    {
      key: 'name',
      sortValue: (account) => account.displayName,
      header: t('external.col.name'),
      render: (account) => (
        <Link to={`/admin/users/${encodeURIComponent(account.userId)}`}>{account.displayName}</Link>
      ),
    },
    {
      key: 'status',
      header: t('external.col.status'),
      render: (account) => <PersonStatusBadge status={account.status} />,
    },
    {
      key: 'expires',
      sortValue: (account) => account.accessExpiresAt,
      header: t('external.col.accessEnds'),
      render: (account) => formatDateTime(locale, account.accessExpiresAt),
    },
  ];
  return (
    <>
      <PageHeader
        title={party.name}
        eyebrow={t('external.title')}
        intro={t(`external.kind.${party.kind}`)}
        actions={
          <>
            <Button onClick={() => setEditing(true)}>{t('external.action.edit')}</Button>
            <Button onClick={() => setToggling(true)}>
              {t(party.active ? 'external.action.deactivate' : 'external.action.activate')}
            </Button>
            {party.active ? (
              <Link
                className="btn btn-primary"
                to={`/admin/users/new?kind=external&party=${encodeURIComponent(party.id)}`}
              >
                {t('external.action.addAccount')}
              </Link>
            ) : null}
          </>
        }
      />
      <dl className="details">
        <dt>{t('external.col.sponsor')}</dt>
        <dd>
          <Link to={`/admin/users/${encodeURIComponent(party.sponsorUserId)}`}>
            {party.sponsorName}
          </Link>
        </dd>
        <dt>{t('external.col.status')}</dt>
        <dd>
          <Badge tone={party.active ? 'success' : 'warning'}>
            {t(party.active ? 'external.status.active' : 'external.status.inactive')}
          </Badge>
        </dd>
        <dt>{t('external.field.notes')}</dt>
        <dd>{party.notes || '–'}</dd>
      </dl>
      {party.active ? null : <p className="field-hint">{t('external.inactiveHint')}</p>}
      <h2>{t('external.accounts')}</h2>
      <DataTable
        caption={t('external.accounts')}
        columns={columns}
        rows={party.accounts}
        rowKey={(account) => account.userId}
        loading={false}
        error={undefined}
        onRetry={loaded.reload}
        emptyText={t('external.accountsEmpty')}
      />
      {editing ? (
        <EditPartyDialog
          party={party}
          onClose={() => setEditing(false)}
          onDone={() => {
            setEditing(false);
            loaded.reload();
          }}
        />
      ) : null}
      {toggling ? (
        <GuardedActionDialog
          title={t(party.active ? 'external.deactivate.title' : 'external.activate.title', {
            name: party.name,
          })}
          confirmLabel={t(party.active ? 'external.action.deactivate' : 'external.action.activate')}
          danger={party.active}
          run={async () => {
            await externalPartiesApi.update(party.id, {
              expectedVersion: party.version,
              active: !party.active,
            });
          }}
          onDone={() => {
            setToggling(false);
            loaded.reload();
          }}
          onClose={() => setToggling(false)}
        >
          <p>{t(party.active ? 'external.deactivate.intro' : 'external.activate.intro')}</p>
        </GuardedActionDialog>
      ) : null}
    </>
  );
}

function EditPartyDialog({
  party,
  onClose,
  onDone,
}: {
  party: ExternalPartyDetail;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(party.name);
  const [kind, setKind] = useState<PartyKind>(party.kind);
  const [notes, setNotes] = useState(party.notes);
  const [sponsor, setSponsor] = useState<PickedPerson | null>({
    id: party.sponsorUserId,
    label: party.sponsorName,
  });
  return (
    <GuardedActionDialog
      title={t('external.edit.title')}
      confirmLabel={t('external.edit.submit')}
      disabled={name.trim() === '' || !sponsor}
      run={async () => {
        await externalPartiesApi.update(party.id, {
          expectedVersion: party.version,
          name: name.trim(),
          kind,
          notes: notes.trim(),
          ...(sponsor ? { sponsorUserId: sponsor.id } : {}),
        });
      }}
      onDone={onDone}
      onClose={onClose}
    >
      <TextField
        label={t('external.field.name')}
        value={name}
        maxLength={200}
        required
        onChange={(event) => setName(event.target.value)}
      />
      <Select
        label={t('external.field.kind')}
        value={kind}
        onChange={(event) => setKind(event.target.value as PartyKind)}
        options={partyKinds.map((value) => ({ value, label: t(`external.kind.${value}`) }))}
      />
      <PersonPicker label={t('external.field.sponsor')} value={sponsor} onChange={setSponsor} />
      <TextArea
        label={t('external.field.notes')}
        hint={t('external.field.notesHint')}
        value={notes}
        maxLength={500}
        rows={3}
        onChange={(event) => setNotes(event.target.value)}
      />
    </GuardedActionDialog>
  );
}
