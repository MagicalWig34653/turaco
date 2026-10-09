import { useState } from 'react';
import { api } from '../../api/client';
import { useAsync } from '../../api/useAsync';
import { useI18n } from '../../i18n/I18nProvider';
import { useSession } from '../../session/SessionProvider';
import { ApiErrorAlert } from '../ApiErrorAlert';
import { Select } from '../Field';
import { ReferencePicker } from '../query/ReferencePicker';
import { useReferenceNames } from '../query/referenceNames';

export type ChooserType = 'user' | 'team' | 'role';
export type ChosenSubject = { type: ChooserType; id: string };
type Role = { id: string; key: string; name: string };

/** Roles are few and not paginated; listing them needs platform.roles.view (the server enforces it). */
export function useRoles(enabled: boolean) {
  return useAsync(
    async (signal) =>
      enabled ? (await api.get<{ items: Role[] }>('/roles', { signal })).items : [],
    [enabled],
  );
}

/** Display names for share and rule subjects; unknown ids fall back to a neutral label. */
export function useSubjectNames(subjects: readonly { type: string; id?: string | undefined }[]) {
  const { t } = useI18n();
  const { can } = useSession();
  const roles = useRoles(can('platform.roles.view') && subjects.some((s) => s.type === 'role'));
  const resolve = useReferenceNames(
    subjects.flatMap((subject) =>
      subject.id && (subject.type === 'user' || subject.type === 'team')
        ? [
            {
              kind: subject.type === 'user' ? ('users' as const) : ('teams' as const),
              id: subject.id,
            },
          ]
        : [],
    ),
  );
  return (type: string, id: string | undefined): string => {
    if (type === 'everyone') return t('views.share.everyone');
    if (!id) return t('query.referenceUnknown');
    if (type === 'role')
      return roles.data?.find((role) => role.id === id)?.name ?? t('views.share.unknownRole');
    return resolve(type === 'user' ? 'users' : 'teams', id) ?? t('query.referenceUnknown');
  };
}

/** Picks one user, team or role: the shared people picker plus a role list. */
export function SubjectChooser({
  types,
  value,
  onChange,
}: {
  types: readonly ChooserType[];
  value: ChosenSubject | null;
  onChange: (subject: ChosenSubject | null) => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const [type, setType] = useState<ChooserType>(types[0] ?? 'user');
  const roles = useRoles(type === 'role' && can('platform.roles.view'));
  const labels: Record<ChooserType, string> = {
    user: t('views.share.type.user'),
    team: t('views.share.type.team'),
    role: t('views.share.type.role'),
  };
  return (
    <div className="subject-chooser">
      <Select
        label={t('views.share.subjectType')}
        value={type}
        options={types.map((candidate) => ({ value: candidate, label: labels[candidate] }))}
        onChange={(event) => {
          setType(event.target.value as ChooserType);
          onChange(null);
        }}
      />
      {type === 'role' ? (
        !can('platform.roles.view') ? (
          <p className="field-hint">{t('views.share.rolesNoPermission')}</p>
        ) : (
          <>
            {roles.error ? <ApiErrorAlert error={roles.error} onRetry={roles.reload} /> : null}
            <fieldset className="query-reference-results" aria-busy={roles.loading}>
              <legend className="visually-hidden">{t('views.share.roleResults')}</legend>
              {(roles.data ?? []).map((role) => (
                <label key={role.id}>
                  <input
                    type="radio"
                    name="view-share-role"
                    checked={value?.type === 'role' && value.id === role.id}
                    onChange={() => onChange({ type: 'role', id: role.id })}
                  />
                  <span>{role.name}</span>
                </label>
              ))}
            </fieldset>
          </>
        )
      ) : (
        <ReferencePicker
          key={type}
          resource={type === 'user' ? 'users' : 'teams'}
          multiple={false}
          value={value?.type === type ? [value.id] : []}
          onChange={([id]) => onChange(id ? { type, id } : null)}
        />
      )}
    </div>
  );
}
