import { useState } from 'react';
import { errorMessageKey } from '../../platform/api/errorMessages';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync, usePagedList } from '../../platform/api/useAsync';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { useLocation } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Badge } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { DataTable, type Column } from '../../platform/ui/DataTable';
import { ConfirmDialog } from '../../platform/ui/Dialog';
import { Checkbox, Select } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { AssignDialog } from './AssignDialog';
import { accessApi } from './api';
import type { RoleAssignment, SubjectType } from './types';

export function RoleAssignmentsScreen() {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const { search } = useLocation();
  const canManage = can('platform.roles.manage');

  const [roleId, setRoleId] = useState(() => new URLSearchParams(search).get('roleId') ?? '');
  const [subjectType, setSubjectType] = useState<SubjectType | ''>('');
  const [includeRevoked, setIncludeRevoked] = useState(false);
  const [assigning, setAssigning] = useState(false);
  const [revoking, setRevoking] = useState<RoleAssignment | null>(null);
  const [revokeBusy, setRevokeBusy] = useState(false);
  const [revokeError, setRevokeError] = useState<ApiError | undefined>(undefined);

  const roles = useAsync((signal) => accessApi.roles(signal), []);
  const list = usePagedList(
    (cursor, signal) =>
      accessApi.roleAssignments({ roleId, subjectType, includeRevoked }, cursor, signal),
    [roleId, subjectType, includeRevoked],
  );

  const revoke = async () => {
    if (!revoking) return;
    setRevokeBusy(true);
    setRevokeError(undefined);
    try {
      await accessApi.revokeRoleAssignment(revoking.id);
      setRevoking(null);
      list.reload();
      roles.reload();
    } catch (cause) {
      setRevokeError(asApiError(cause));
    } finally {
      setRevokeBusy(false);
    }
  };

  const columns: Column<RoleAssignment>[] = [
    { key: 'role', header: t('assignments.col.role'), render: (a) => <code>{a.roleKey}</code> },
    {
      key: 'subject',
      header: t('assignments.col.subject'),
      render: (a) => a.subjectDisplayName || <code>{a.subjectId}</code>,
    },
    {
      key: 'type',
      header: t('assignments.col.subjectType'),
      render: (a) => t(a.subjectType === 'user' ? 'subject.user' : 'subject.directory_group'),
    },
    {
      key: 'created',
      header: t('assignments.col.created'),
      render: (a) => formatDateTime(locale, a.createdAt),
    },
    {
      key: 'status',
      header: t('assignments.col.status'),
      render: (a) =>
        a.revokedAt ? (
          <Badge>{t('assignments.revokedAt', { date: formatDateTime(locale, a.revokedAt) })}</Badge>
        ) : (
          <Badge tone="info">{t('assignments.active')}</Badge>
        ),
    },
    ...(canManage
      ? [
          {
            key: 'actions',
            header: t('assignments.col.actions'),
            render: (a: RoleAssignment) =>
              a.revokedAt ? null : (
                <Button
                  variant="danger"
                  onClick={() => {
                    setRevokeError(undefined);
                    setRevoking(a);
                  }}
                  aria-label={t('assignments.revoke.for', {
                    role: a.roleKey,
                    subject: a.subjectDisplayName || a.subjectId,
                  })}
                >
                  {t('assignments.revoke')}
                </Button>
              ),
          } satisfies Column<RoleAssignment>,
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={t('nav.roleAssignments')}
        intro={t('assignments.intro')}
        actions={
          canManage && roles.data ? (
            <Button variant="primary" onClick={() => setAssigning(true)}>
              {t('assignments.assign')}
            </Button>
          ) : null
        }
      />
      {roles.error ? <ApiErrorAlert error={roles.error} onRetry={roles.reload} /> : null}
      <form
        className="filters"
        onSubmit={(event) => event.preventDefault()}
        aria-label={t('filters.title')}
      >
        <Select
          label={t('assignments.filter.role')}
          value={roleId}
          onChange={(event) => setRoleId(event.target.value)}
          options={[
            { value: '', label: t('filters.all') },
            ...(roles.data?.items ?? []).map((role) => ({ value: role.id, label: role.name })),
          ]}
        />
        <Select
          label={t('assignments.filter.subjectType')}
          value={subjectType}
          onChange={(event) => setSubjectType(event.target.value as SubjectType | '')}
          options={[
            { value: '', label: t('filters.all') },
            { value: 'user', label: t('subject.user') },
            { value: 'directory_group', label: t('subject.directory_group') },
          ]}
        />
        <Checkbox
          label={t('assignments.filter.includeRevoked')}
          checked={includeRevoked}
          onChange={(event) => setIncludeRevoked(event.target.checked)}
        />
      </form>
      <DataTable
        caption={t('nav.roleAssignments')}
        columns={columns}
        rows={list.items}
        rowKey={(a) => a.id}
        loading={list.loading}
        error={list.error}
        onRetry={list.reload}
        emptyText={t('assignments.empty')}
        hasMore={list.hasMore}
        loadingMore={list.loadingMore}
        loadMoreError={list.loadMoreError}
        onLoadMore={list.loadMore}
      />
      {assigning && roles.data ? (
        <AssignDialog
          roles={roles.data.items}
          initialRoleId={roleId}
          onClose={() => setAssigning(false)}
          onAssigned={() => {
            setAssigning(false);
            list.reload();
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
                role: revoking.roleKey,
                subject: revoking.subjectDisplayName || revoking.subjectId,
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
    </>
  );
}
