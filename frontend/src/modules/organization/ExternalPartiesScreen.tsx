import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { externalPartiesApi, partyKinds, type ExternalParty, type PartyKind } from './externalApi';
import { PersonPicker, type PickedPerson } from './PersonPicker';

/** The External Parties of the installation: companies whose staff hold restricted external accounts. */
export function ExternalPartiesScreen() {
  const { t } = useI18n();
  const parties = useAsync((signal) => externalPartiesApi.list(signal), []);
  const [creating, setCreating] = useState(false);
  const [showInactive, setShowInactive] = useState(false);
  const rows = (parties.data?.items ?? []).filter((party) => showInactive || party.active);
  const columns: Column<ExternalParty>[] = [
    {
      key: 'name',
      sortValue: (party) => party.name,
      header: t('external.col.name'),
      render: (party) => (
        <Link to={`/admin/external-parties/${encodeURIComponent(party.id)}`}>{party.name}</Link>
      ),
    },
    {
      key: 'kind',
      header: t('external.col.kind'),
      render: (party) => t(`external.kind.${party.kind}`),
    },
    { key: 'sponsor', header: t('external.col.sponsor'), render: (party) => party.sponsorName },
    {
      key: 'accounts',
      header: t('external.col.accounts'),
      sortValue: (party) => party.accountCount,
      render: (party) => party.accountCount,
    },
    {
      key: 'status',
      header: t('external.col.status'),
      render: (party) => (
        <Badge tone={party.active ? 'success' : 'warning'}>
          {t(party.active ? 'external.status.active' : 'external.status.inactive')}
        </Badge>
      ),
    },
  ];
  return (
    <>
      <PageHeader
        title={t('external.title')}
        eyebrow={t('sidebar.section.admin')}
        intro={t('external.intro')}
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            {t('external.create.action')}
          </Button>
        }
      />
      <label className="checkbox-inline">
        <input
          type="checkbox"
          checked={showInactive}
          onChange={(event) => setShowInactive(event.target.checked)}
        />{' '}
        {t('external.showInactive')}
      </label>
      <DataTable
        caption={t('external.title')}
        columns={columns}
        rows={rows}
        rowKey={(party) => party.id}
        loading={parties.loading}
        error={parties.error}
        onRetry={parties.reload}
        emptyText={t('external.empty')}
      />
      {creating ? (
        <CreatePartyDialog
          onClose={() => setCreating(false)}
          onCreated={(party) => {
            setCreating(false);
            navigate(`/admin/external-parties/${encodeURIComponent(party.id)}`);
          }}
        />
      ) : null}
    </>
  );
}

function CreatePartyDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (party: ExternalParty) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [kind, setKind] = useState<PartyKind>('vendor');
  const [notes, setNotes] = useState('');
  const [sponsor, setSponsor] = useState<PickedPerson | null>(null);
  return (
    <GuardedActionDialog
      title={t('external.create.title')}
      confirmLabel={t('external.create.submit')}
      disabled={name.trim() === '' || !sponsor}
      run={async () => {
        if (!sponsor) return;
        onCreated(
          await externalPartiesApi.create({
            kind,
            name: name.trim(),
            sponsorUserId: sponsor.id,
            ...(notes.trim() ? { notes: notes.trim() } : {}),
          }),
        );
      }}
      onDone={() => undefined}
      onClose={onClose}
    >
      <TextField
        label={t('external.field.name')}
        value={name}
        maxLength={200}
        required
        autoFocus
        onChange={(event) => setName(event.target.value)}
      />
      <Select
        label={t('external.field.kind')}
        value={kind}
        onChange={(event) => setKind(event.target.value as PartyKind)}
        options={partyKinds.map((value) => ({ value, label: t(`external.kind.${value}`) }))}
      />
      <PersonPicker label={t('external.field.sponsor')} value={sponsor} onChange={setSponsor} />
      <p className="field-hint">{t('external.field.sponsorHint')}</p>
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
