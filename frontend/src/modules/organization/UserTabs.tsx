import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { errorMessageKey } from '../../platform/api/errorMessages';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert, Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { TableDate } from '../../platform/ui/TableDate';
import { accessApi } from '../access/api';
import { AssignDialog } from '../access/AssignDialog';
import { ExpiryCell } from '../access/RoleAssignmentsScreen';
import type { RoleAssignment } from '../access/types';
import { peopleAdminApi } from './adminApi';
import type { PersonDetail, TeamMemberRow, TeamRole, TeamRow } from './adminTypes';

const MAX_TEAMS = 60;

type Membership = { team: TeamRow; member: TeamMemberRow };

export function UserTeamsTab({ person }: { person: PersonDetail }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const canManage = can('organization.teams.manage');
  const isSelf = session?.userId === person.id;
  const data = useAsync(
    async (signal) => {
      const all = (await peopleAdminApi.teams('', signal)).filter((team) => team.active);
      const checked = all.slice(0, MAX_TEAMS);
      const loaded = await Promise.all(
        checked.map(async (team) => ({
          team,
          members: await peopleAdminApi.teamMembers(team.id, signal),
        })),
      );
      const memberships: Membership[] = [];
      for (const { team, members } of loaded) {
        const member = members.find((entry) => entry.userId === person.id);
        if (member) memberships.push({ team, member });
      }
      return { all, memberships, truncated: all.length > checked.length };
    },
    [person.id],
  );
  const [teamId, setTeamId] = useState('');
  const [role, setRole] = useState<TeamRole>('member');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [removing, setRemoving] = useState<Membership | null>(null);

  const act = async (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await work();
      data.reload();
    } catch (cause) {
      setError(asApiError(cause));
    } finally {
      setBusy(false);
    }
  };

  if (data.loading && !data.data) return <p role="status">{t('state.loading')}</p>;
  if (data.error) return <ApiErrorAlert error={data.error} onRetry={data.reload} />;
  const memberships = data.data?.memberships ?? [];
  const memberOf = new Set(memberships.map((entry) => entry.team.id));
  const candidates = (data.data?.all ?? []).filter((team) => !memberOf.has(team.id));

  return (
    <section aria-label={t('people.teams.title')}>
      <p className="subtitle">{t('people.teams.intro')}</p>
      {isSelf ? <Alert kind="info">{t('people.teams.self')}</Alert> : null}
      {data.data?.truncated ? (
        <Alert kind="warning">{t('people.teams.truncated', { count: MAX_TEAMS })}</Alert>
      ) : null}
      {error ? <ApiErrorAlert error={error} /> : null}
      {memberships.length === 0 ? (
        <p className="empty">{t('people.teams.empty')}</p>
      ) : (
        <ul className="adm-list">
          {memberships.map(({ team, member }) => (
            <li key={team.id}>
              <Link to={`/admin/teams/${encodeURIComponent(team.id)}`}>{team.name}</Link>{' '}
              <Badge tone={member.role === 'lead' ? 'info' : 'neutral'}>
                {t(member.role === 'lead' ? 'teams.role.lead' : 'teams.role.member')}
              </Badge>{' '}
              <span className="adm-sub">
                {t('teams.member.since')} <TableDate value={member.validFrom} />
              </span>
              {canManage && !isSelf ? (
                <span className="adm-row-actions">
                  <Button
                    busy={busy}
                    onClick={() =>
                      void act(() =>
                        peopleAdminApi.setTeamMemberRole(
                          team.id,
                          person.id,
                          member.role === 'lead' ? 'member' : 'lead',
                        ),
                      )
                    }
                  >
                    {t(
                      member.role === 'lead' ? 'teams.action.makeMember' : 'teams.action.makeLead',
                    )}
                  </Button>
                  <Button variant="danger" onClick={() => setRemoving({ team, member })}>
                    {t('teams.action.remove')}
                  </Button>
                </span>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {canManage && !isSelf && candidates.length > 0 ? (
        <form
          className="adm-inline-form"
          onSubmit={(event) => {
            event.preventDefault();
            if (teamId)
              void act(() => peopleAdminApi.addTeamMember(teamId, person.id, role)).then(() =>
                setTeamId(''),
              );
          }}
        >
          <Select
            label={t('people.teams.add')}
            value={teamId}
            onChange={(event) => setTeamId(event.target.value)}
            options={[
              { value: '', label: t('people.teams.choose') },
              ...candidates.map((team) => ({ value: team.id, label: team.name })),
            ]}
          />
          <Select
            label={t('teams.col.role')}
            value={role}
            onChange={(event) => setRole(event.target.value as TeamRole)}
            options={[
              { value: 'member', label: t('teams.role.member') },
              { value: 'lead', label: t('teams.role.lead') },
            ]}
          />
          <Button type="submit" variant="primary" busy={busy} disabled={!teamId}>
            {t('people.teams.addAction')}
          </Button>
        </form>
      ) : null}
      {removing ? (
        <ConfirmDialog
          title={t('teams.remove.title')}
          message={
            <p>
              {t('teams.remove.message', { name: person.displayName, team: removing.team.name })}
            </p>
          }
          confirmLabel={t('teams.action.remove')}
          danger
          busy={busy}
          error={error ? t(errorMessageKey(error)) : undefined}
          onConfirm={() =>
            void act(() => peopleAdminApi.removeTeamMember(removing.team.id, person.id)).then(() =>
              setRemoving(null),
            )
          }
          onCancel={() => setRemoving(null)}
        />
      ) : null}
    </section>
  );
}

export function UserRolesTab({ person }: { person: PersonDetail }) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const canManage = can('platform.roles.manage');
  const roles = useAsync((signal) => accessApi.roles(signal), []);
  const assignments = useAsync(
    (signal) =>
      accessApi.roleAssignments({ subjectType: 'user', subjectId: person.id }, undefined, signal),
    [person.id],
  );
  const [assigning, setAssigning] = useState(false);
  const [revoking, setRevoking] = useState<RoleAssignment | null>(null);
  const [revokeBusy, setRevokeBusy] = useState(false);
  const [revokeError, setRevokeError] = useState<ApiError | undefined>(undefined);
  const roleNames = new Map((roles.data?.items ?? []).map((role) => [role.id, role]));

  const revoke = async () => {
    if (!revoking) return;
    setRevokeBusy(true);
    setRevokeError(undefined);
    try {
      await accessApi.revokeRoleAssignment(revoking.id);
      setRevoking(null);
      assignments.reload();
      roles.reload();
    } catch (cause) {
      setRevokeError(asApiError(cause));
    } finally {
      setRevokeBusy(false);
    }
  };

  if (assignments.loading && !assignments.data) return <p role="status">{t('state.loading')}</p>;
  if (assignments.error)
    return <ApiErrorAlert error={assignments.error} onRetry={assignments.reload} />;
  const items = assignments.data?.items ?? [];
  const isSelf = session?.userId === person.id;
  return (
    <section aria-label={t('people.roles.title')}>
      <p className="subtitle">{t('people.roles.intro')}</p>
      {isSelf ? <Alert kind="info">{t('people.roles.self')}</Alert> : null}
      {person.source === 'local' ? <Alert kind="info">{t('people.roles.localHint')}</Alert> : null}
      {canManage && roles.data ? (
        <div className="form-actions">
          <Button variant="primary" onClick={() => setAssigning(true)} disabled={isSelf}>
            {t('people.roles.assign')}
          </Button>
        </div>
      ) : null}
      {items.length === 0 ? (
        <p className="empty">{t('people.roles.empty')}</p>
      ) : (
        <ul className="adm-list">
          {items.map((assignment) => {
            const role = roleNames.get(assignment.roleId);
            return (
              <li key={assignment.id}>
                <Link to={`/admin/roles/${encodeURIComponent(assignment.roleId)}`}>
                  {role?.name ?? assignment.roleKey}
                </Link>{' '}
                <span className="adm-sub">
                  {t('assignments.col.created')} <TableDate value={assignment.createdAt} />
                </span>{' '}
                <span className="adm-sub">
                  {t('assignments.col.expires')}: <ExpiryCell expiresAt={assignment.expiresAt} />
                </span>
                {canManage ? (
                  <span className="adm-row-actions">
                    <Button variant="danger" onClick={() => setRevoking(assignment)}>
                      {t('assignments.revoke')}
                    </Button>
                  </span>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      <p>
        <Link to={`/admin/role-assignments`}>{t('people.roles.allAssignments')}</Link>
      </p>
      {assigning && roles.data ? (
        <AssignDialog
          roles={roles.data.items}
          initialRoleId=""
          fixedSubject={{ id: person.id, label: person.displayName }}
          onClose={() => setAssigning(false)}
          onAssigned={() => {
            setAssigning(false);
            assignments.reload();
            roles.reload();
          }}
        />
      ) : null}
      {revoking ? (
        <ConfirmDialog
          title={t('assignments.revoke.title')}
          message={
            <p>
              {t('assignments.revoke.message', {
                role: roleNames.get(revoking.roleId)?.name ?? revoking.roleKey,
                subject: person.displayName,
              })}
            </p>
          }
          confirmLabel={t('assignments.revoke')}
          danger
          busy={revokeBusy}
          error={revokeError ? t(errorMessageKey(revokeError)) : undefined}
          onConfirm={() => void revoke()}
          onCancel={() => setRevoking(null)}
        />
      ) : null}
    </section>
  );
}
