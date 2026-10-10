import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { Select, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { endOfDayIso, riskCounts, validateExpiry } from './accessModel';
import { accessApi } from './api';
import { SubjectPicker, type Subject } from './SubjectPicker';
import type { Role, SubjectType } from './types';
import { useActor } from './useActor';

type Props = {
  roles: readonly Role[];
  initialRoleId: string;
  /** A person fixed by the page (the User detail); hides the subject choice. */
  fixedSubject?: Subject;
  onClose: () => void;
  onAssigned: () => void;
};

const todayPlus = (days: number) => {
  const date = new Date(Date.now() + days * 86_400_000);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
};

export function AssignDialog({ roles, initialRoleId, fixedSubject, onClose, onAssigned }: Props) {
  const { t } = useI18n();
  const actor = useActor();
  const permissions = useAsync((signal) => accessApi.permissions(signal), []);
  const [roleId, setRoleId] = useState(initialRoleId || roles[0]?.id || '');
  const [subjectType, setSubjectType] = useState<SubjectType>('user');
  const [subject, setSubject] = useState<Subject | null>(fixedSubject ?? null);
  const [expiry, setExpiry] = useState('');

  const role = roles.find((candidate) => candidate.id === roleId);
  const byName = new Map(
    (permissions.data?.items ?? []).map((permission) => [permission.name, permission]),
  );
  const counts = riskCounts(role?.permissions ?? [], byName);
  const problem = role ? validateExpiry(expiry, role, byName, Date.now()) : null;
  const isAdminRole = role?.key === 'platform-administrator';

  return (
    <GuardedActionDialog
      title={t('assign.title')}
      confirmLabel={t('assign.submit')}
      disabled={!subject || !roleId || problem !== null}
      run={async (extras) => {
        if (!subject) return;
        const expiresAt = expiry ? endOfDayIso(expiry) : undefined;
        await accessApi.assignRole({
          roleId,
          subjectType,
          subjectId: subject.id,
          ...(expiresAt ? { expiresAt } : {}),
          ...(extras.acknowledgedRules ? { acknowledgedRules: extras.acknowledgedRules } : {}),
          ...(extras.reason ? { reason: extras.reason } : {}),
        });
      }}
      onDone={onAssigned}
      onClose={onClose}
    >
      <Select
        label={t('assign.role')}
        value={roleId}
        onChange={(event) => setRoleId(event.target.value)}
        options={roles.map((candidate) => ({
          value: candidate.id,
          label: `${candidate.name} (${candidate.key})`,
        }))}
      />
      {role ? (
        <p className="field-hint">
          {t('assign.roleSummary', {
            count: role.permissions.length,
            elevated: counts.elevated,
            high: counts.high,
          })}
        </p>
      ) : null}
      {counts.high > 0 && !actor.isAdministrator ? (
        <Alert kind="warning">{t('assign.highRisk.administratorOnly')}</Alert>
      ) : null}
      {fixedSubject ? (
        <p>{t('assign.fixedSubject', { name: fixedSubject.label })}</p>
      ) : (
        <>
          <Select
            label={t('assign.subjectType')}
            value={subjectType}
            onChange={(event) => {
              setSubjectType(event.target.value as SubjectType);
              setSubject(null);
            }}
            options={[
              { value: 'user', label: t('subject.user') },
              { value: 'directory_group', label: t('subject.directory_group') },
            ]}
          />
          {subjectType === 'directory_group' ? (
            <Alert kind="warning">{t('assign.group.warning')}</Alert>
          ) : null}
          <SubjectPicker
            key={subjectType}
            subjectType={subjectType}
            value={subject}
            onChange={setSubject}
          />
        </>
      )}
      <TextField
        label={t('assign.expiry')}
        hint={
          isAdminRole
            ? t('assign.expiry.adminHint')
            : counts.high > 0
              ? t('assign.expiry.highRiskHint')
              : t('assign.expiry.hint')
        }
        type="date"
        value={expiry}
        min={todayPlus(1)}
        disabled={isAdminRole}
        onChange={(event) => setExpiry(event.target.value)}
        error={problem ? t(`assign.expiry.error.${problem}`) : undefined}
      />
    </GuardedActionDialog>
  );
}
