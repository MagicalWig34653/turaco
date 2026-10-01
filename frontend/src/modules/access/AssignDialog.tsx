import { useState } from 'react';
import type { FormEvent } from 'react';
import type { ApiError } from '../../platform/api/client';
import { endpoints } from '../../platform/api/endpoints';
import type { Role, SubjectType } from '../../platform/api/types';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { SubjectPicker, type Subject } from './SubjectPicker';

type Props = {
  roles: readonly Role[];
  initialRoleId: string;
  onClose: () => void;
  onAssigned: () => void;
};

export function AssignDialog({ roles, initialRoleId, onClose, onAssigned }: Props) {
  const { t } = useI18n();
  const [roleId, setRoleId] = useState(initialRoleId || roles[0]?.id || '');
  const [subjectType, setSubjectType] = useState<SubjectType>('user');
  const [subject, setSubject] = useState<Subject | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!subject || !roleId) return;
    setBusy(true);
    setError(undefined);
    try {
      await endpoints.assignRole({ roleId, subjectType, subjectId: subject.id });
      onAssigned();
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };

  return (
    <Dialog title={t('assign.title')} onClose={onClose} wide>
      <form className="form" onSubmit={(event) => void submit(event)}>
        {error ? <ApiErrorAlert error={error} /> : null}
        <Select
          label={t('assign.role')}
          value={roleId}
          onChange={(event) => setRoleId(event.target.value)}
          options={roles.map((role) => ({ value: role.id, label: `${role.name} (${role.key})` }))}
        />
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
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button type="submit" variant="primary" busy={busy} disabled={!subject || !roleId}>
            {t('assign.submit')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
