import { useRef, useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link, navigate } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import type { MenuItem } from '../../platform/ui/ContextMenu';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { Select, TextArea, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { PageHeader } from '../../platform/ui/PageHeader';
import { TableDate } from '../../platform/ui/TableDate';
import { peopleAdminApi } from './adminApi';
import type { TeamMemberRow, TeamRole, TeamRow } from './adminTypes';
import { PersonPicker, type PickedPerson } from './PersonPicker';
import { TeamDeactivateDialog } from './TeamsScreen';

export function TeamDetailScreen({ id }: { id: string }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const canManage = can('organization.teams.manage');
  const canViewPeople = can('organization.view');
  const team = useAsync((signal) => peopleAdminApi.team(id, signal), [id]);
  const members = useAsync((signal) => peopleAdminApi.teamMembers(id, signal), [id]);
  const [editing, setEditing] = useState(false);
  const [deactivating, setDeactivating] = useState(false);
  const [removing, setRemoving] = useState<TeamMemberRow | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [pick, setPick] = useState<PickedPerson | null>(null);
  const [role, setRole] = useState<TeamRole>('member');

  const self = session?.userId;
  const isMember = (members.data ?? []).some((member) => member.userId === self);

  const act = async (work: () => Promise<unknown>): Promise<boolean> => {
    setBusy(true);
    setError(undefined);
    try {
      await work();
      members.reload();
      team.reload();
      return true;
    } catch (cause) {
      setError(asApiError(cause));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const columns: Column<TeamMemberRow>[] = [
    {
      key: 'name',
      sortValue: (member) => member.displayName,
      header: t('teams.col.member'),
      render: (member) =>
        canViewPeople ? (
          <Link to={`/admin/users/${encodeURIComponent(member.userId)}`}>{member.displayName}</Link>
        ) : (
          member.displayName
        ),
    },
    {
      key: 'role',
      header: t('teams.col.role'),
      render: (member) => (
        <Badge tone={member.role === 'lead' ? 'info' : 'neutral'}>
          {t(member.role === 'lead' ? 'teams.role.lead' : 'teams.role.member')}
        </Badge>
      ),
    },
    {
      key: 'since',
      header: t('teams.col.since'),
      render: (member) => <TableDate value={member.validFrom} />,
    },
  ];
  const rowActions = (member: TeamMemberRow): MenuItem[] => {
    const own = member.userId === self;
    const blocked = own ? t('people.error.teamMembershipSelf') : undefined;
    const common = (
      item: Omit<Extract<MenuItem, { onSelect: () => void }>, 'disabledReason'>,
    ): MenuItem => (blocked ? { ...item, disabledReason: blocked } : item);
    return [
      ...(canViewPeople
        ? [
            {
              id: 'open',
              label: t('contextMenu.open'),
              onSelect: () => navigate(`/admin/users/${encodeURIComponent(member.userId)}`),
            } satisfies MenuItem,
          ]
        : []),
      ...(canManage
        ? [
            common({
              id: 'role',
              label: t(
                member.role === 'lead' ? 'teams.action.makeMember' : 'teams.action.makeLead',
              ),
              onSelect: () =>
                void act(() =>
                  peopleAdminApi.setTeamMemberRole(
                    id,
                    member.userId,
                    member.role === 'lead' ? 'member' : 'lead',
                  ),
                ),
            }),
            common({
              id: 'remove',
              label: t('teams.action.remove'),
              danger: true,
              onSelect: () => setRemoving(member),
            }),
          ]
        : []),
    ];
  };

  const data = team.data;
  return (
    <>
      <PageHeader
        title={data?.name ?? t('teams.detail.title')}
        eyebrow={t('teams.title')}
        intro={data?.description || undefined}
        actions={
          <>
            {canManage && data ? (
              <>
                <Button onClick={() => setEditing(true)}>{t('teams.action.edit')}</Button>
                {data.active ? (
                  <Button variant="danger" onClick={() => setDeactivating(true)}>
                    {t('teams.action.deactivate')}
                  </Button>
                ) : (
                  <Button onClick={() => void act(() => peopleAdminApi.activateTeam(id))}>
                    {t('teams.action.activate')}
                  </Button>
                )}
              </>
            ) : null}
            <Link to="/admin/teams" className="btn btn-secondary">
              {t('teams.back')}
            </Link>
          </>
        }
      />
      {team.error ? <ApiErrorAlert error={team.error} onRetry={team.reload} /> : null}
      {data && !data.active ? <Alert kind="warning">{t('teams.inactive.notice')}</Alert> : null}
      {isMember ? <Alert kind="info">{t('teams.self.notice')}</Alert> : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      <h2>{t('teams.members.title')}</h2>
      <DataTable
        caption={t('teams.members.title')}
        columns={columns}
        rows={members.data ?? []}
        rowKey={(member) => member.userId}
        rowActions={rowActions}
        loading={members.loading}
        error={members.error}
        onRetry={members.reload}
        emptyText={t('teams.members.empty')}
      />
      {canManage && data?.active ? (
        <section className="adm-card" aria-label={t('teams.members.add')}>
          <h3>{t('teams.members.add')}</h3>
          <p className="field-hint">{t('teams.members.leadHint')}</p>
          <PersonPicker
            value={pick}
            onChange={setPick}
            exclude={[
              ...(members.data ?? []).map((member) => member.userId),
              ...(self ? [self] : []),
            ]}
          />
          <div className="adm-inline-form">
            <Select
              label={t('teams.col.role')}
              value={role}
              onChange={(event) => setRole(event.target.value as TeamRole)}
              options={[
                { value: 'member', label: t('teams.role.member') },
                { value: 'lead', label: t('teams.role.lead') },
              ]}
            />
            <Button
              variant="primary"
              busy={busy}
              disabled={!pick}
              onClick={() => {
                if (pick)
                  void act(() => peopleAdminApi.addTeamMember(id, pick.id, role)).then(
                    (ok) => ok && setPick(null),
                  );
              }}
            >
              {t('teams.members.addAction')}
            </Button>
          </div>
        </section>
      ) : null}
      {editing && data ? (
        <EditTeamDialog
          team={data}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            team.reload();
          }}
        />
      ) : null}
      {deactivating && data ? (
        <TeamDeactivateDialog
          team={data}
          onClose={() => setDeactivating(false)}
          onDone={() => {
            setDeactivating(false);
            team.reload();
          }}
        />
      ) : null}
      {removing ? (
        <ConfirmDialog
          title={t('teams.remove.title')}
          message={
            <p>
              {t('teams.remove.message', { name: removing.displayName, team: data?.name ?? '' })}
            </p>
          }
          confirmLabel={t('teams.action.remove')}
          danger
          busy={busy}
          error={error ? t(errorMessageKey(error)) : undefined}
          onConfirm={() =>
            void act(() => peopleAdminApi.removeTeamMember(id, removing.userId)).then(
              (ok) => ok && setRemoving(null),
            )
          }
          onCancel={() => setRemoving(null)}
        />
      ) : null}
    </>
  );
}

function EditTeamDialog({
  team,
  onClose,
  onSaved,
}: {
  team: TeamRow;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description);
  const changed = name.trim() !== team.name || description.trim() !== team.description;
  const progress = useRef({ version: team.version, nameDone: false });
  return (
    <GuardedActionDialog
      title={t('teams.edit.title')}
      confirmLabel={t('action.save')}
      disabled={!changed || name.trim() === ''}
      run={async () => {
        // The API changes either the name or the description per request.
        const state = progress.current;
        if (name.trim() !== team.name && !state.nameDone) {
          const updated = await peopleAdminApi.updateTeam(team.id, {
            name: name.trim(),
            expectedVersion: state.version,
          });
          state.version = updated.version;
          state.nameDone = true;
        }
        if (description.trim() !== team.description) {
          await peopleAdminApi.updateTeam(team.id, {
            description: description.trim(),
            expectedVersion: state.version,
          });
        }
      }}
      onDone={onSaved}
      onClose={onClose}
    >
      <TextField
        label={t('teams.field.name')}
        value={name}
        maxLength={120}
        required
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
