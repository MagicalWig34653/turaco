import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import type { MenuItem } from '../../platform/ui/ContextMenu';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { FilterBar } from '../../platform/ui/FilterBar';
import { Checkbox, TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { useDebouncedValue } from '../../platform/ui/hooks';
import { peopleAdminApi } from './adminApi';
import type { TeamRow } from './adminTypes';

export function TeamDeactivateDialog({
  team,
  onClose,
  onDone,
}: {
  team: TeamRow;
  onClose: () => void;
  onDone: () => void;
}) {
  const { t } = useI18n();
  return (
    <GuardedActionDialog
      title={t('teams.deactivate.title', { name: team.name })}
      confirmLabel={t('teams.deactivate.confirm')}
      danger
      run={async (extras) => {
        await peopleAdminApi.deactivateTeam(team.id, extras.confirmImpact === true);
      }}
      onDone={onDone}
      onClose={onClose}
    >
      <p>{t('teams.deactivate.intro', { name: team.name })}</p>
    </GuardedActionDialog>
  );
}

export function TeamsScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const canManage = can('organization.teams.manage');
  const [search, setSearch] = useState('');
  const [showInactive, setShowInactive] = useState(false);
  const debounced = useDebouncedValue(search.trim(), 250);
  const teams = useAsync((signal) => peopleAdminApi.teams(debounced, signal), [debounced]);
  const [creating, setCreating] = useState(false);
  const [deactivating, setDeactivating] = useState<TeamRow | null>(null);
  const [actionError, setActionError] = useState(false);

  const rows = (teams.data ?? []).filter((team) => showInactive || team.active);
  const columns: Column<TeamRow>[] = [
    {
      key: 'name',
      sortValue: (team) => team.name,
      header: t('teams.col.name'),
      render: (team) => <Link to={`/admin/teams/${encodeURIComponent(team.id)}`}>{team.name}</Link>,
    },
    {
      key: 'description',
      header: t('teams.col.description'),
      render: (team) => team.description || '–',
    },
    {
      key: 'leads',
      header: t('teams.col.leads'),
      render: (team) =>
        team.leads && team.leads.length > 0
          ? team.leads.map((lead) => lead.displayName).join(', ')
          : '–',
    },
    {
      key: 'status',
      header: t('people.col.status'),
      render: (team) => (
        <Badge tone={team.active ? 'success' : 'warning'}>
          {t(team.active ? 'teams.status.active' : 'teams.status.inactive')}
        </Badge>
      ),
    },
  ];
  const rowActions = (team: TeamRow): MenuItem[] => [
    {
      id: 'open',
      label: t('contextMenu.open'),
      onSelect: () => navigate(`/admin/teams/${encodeURIComponent(team.id)}`),
    },
    ...(canManage
      ? [
          team.active
            ? ({
                id: 'deactivate',
                label: t('teams.action.deactivate'),
                danger: true,
                onSelect: () => setDeactivating(team),
              } satisfies MenuItem)
            : ({
                id: 'activate',
                label: t('teams.action.activate'),
                onSelect: () => {
                  setActionError(false);
                  void peopleAdminApi
                    .activateTeam(team.id)
                    .then(teams.reload, () => setActionError(true));
                },
              } satisfies MenuItem),
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('teams.title')}
        eyebrow={t('sidebar.section.admin')}
        intro={t('teams.intro')}
        actions={
          canManage ? (
            <Button variant="primary" onClick={() => setCreating(true)}>
              {t('teams.create.action')}
            </Button>
          ) : null
        }
      />
      <FilterBar role="search" onSubmit={(event) => event.preventDefault()}>
        <TextField
          label={t('teams.search')}
          type="search"
          value={search}
          maxLength={100}
          onChange={(event) => setSearch(event.target.value)}
        />
        <Checkbox
          label={t('teams.showInactive')}
          checked={showInactive}
          onChange={(event) => setShowInactive(event.target.checked)}
        />
      </FilterBar>
      {actionError ? (
        <p className="field-error" role="alert">
          {t('error.generic')}
        </p>
      ) : null}
      <DataTable
        caption={t('teams.title')}
        columns={columns}
        rows={rows}
        rowKey={(team) => team.id}
        rowActions={rowActions}
        loading={teams.loading}
        error={teams.error}
        onRetry={teams.reload}
        emptyText={t('teams.empty')}
      />
      {creating ? (
        <CreateTeamDialog
          onClose={() => setCreating(false)}
          onCreated={(team) => {
            setCreating(false);
            navigate(`/admin/teams/${encodeURIComponent(team.id)}`);
          }}
        />
      ) : null}
      {deactivating ? (
        <TeamDeactivateDialog
          team={deactivating}
          onClose={() => setDeactivating(null)}
          onDone={() => {
            setDeactivating(null);
            teams.reload();
          }}
        />
      ) : null}
    </>
  );
}

function CreateTeamDialog({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (team: TeamRow) => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  return (
    <GuardedActionDialog
      title={t('teams.create.title')}
      confirmLabel={t('teams.create.submit')}
      disabled={name.trim() === ''}
      run={async () => {
        let team = await peopleAdminApi.createTeam(name.trim());
        if (description.trim()) {
          team = await peopleAdminApi.updateTeam(team.id, {
            description: description.trim(),
            expectedVersion: team.version,
          });
        }
        onCreated(team);
      }}
      onDone={() => undefined}
      onClose={onClose}
    >
      <TextField
        label={t('teams.field.name')}
        value={name}
        maxLength={120}
        required
        autoFocus
        onChange={(event) => setName(event.target.value)}
      />
      <TextArea
        label={t('teams.field.description')}
        hint={t('teams.field.descriptionHint')}
        value={description}
        maxLength={500}
        rows={3}
        onChange={(event) => setDescription(event.target.value)}
      />
    </GuardedActionDialog>
  );
}
