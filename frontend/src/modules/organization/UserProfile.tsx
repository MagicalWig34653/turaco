import { useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { Select, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { TableDate } from '../../platform/ui/TableDate';
import { peopleAdminApi } from './adminApi';
import { isFieldLocked, ownerOf, profileChanges, looksLikeEmail } from './adminModel';
import type { FieldOwner, PersonDetail, PersonRow } from './adminTypes';
import { PersonPicker, type PickedPerson } from './PersonPicker';
import { PersonStatusBadge } from './PersonBadges';
import type { useOrgLookups } from './orgLookups';

type Lookups = ReturnType<typeof useOrgLookups>;

/** Who owns an attribute and how fresh it is, as text next to the value. */
export function OwnerChip({ field }: { field: FieldOwner }) {
  const { t } = useI18n();
  if (field.owner === 'directory') {
    return (
      <span className="adm-chip adm-chip-directory">
        {t('people.owner.directory')}
        {field.source && field.source !== 'turaco' ? ` · ${field.source}` : ''}
        {field.observedAt ? (
          <>
            {' · '}
            {t('people.owner.seen')} <TableDate value={field.observedAt} />
          </>
        ) : null}
      </span>
    );
  }
  return <span className="adm-chip">{t('people.owner.turaco')}</span>;
}

function Row({
  label,
  value,
  field,
}: {
  label: string;
  value: ReactNode;
  field?: FieldOwner | undefined;
}) {
  return (
    <div className="adm-row">
      <dt>{label}</dt>
      <dd>
        <span className="adm-value">{value || '–'}</span>
        {field ? <OwnerChip field={field} /> : null}
      </dd>
    </div>
  );
}

export function ProfileCard({ person, lookups }: { person: PersonDetail; lookups: Lookups }) {
  const { t, locale } = useI18n();
  const { can } = useSession();
  const details = can('organization.users.view_details');
  const managerId = person.managerUserId ?? undefined;
  const manager = useAsync(
    (signal) =>
      managerId && details
        ? peopleAdminApi.user(managerId, signal).catch(() => undefined)
        : Promise.resolve(undefined),
    [managerId, details],
  );
  const owner = (key: string) => ownerOf(person.fields, key);
  return (
    <section className="adm-card" aria-label={t('people.profile.title')}>
      <h2>{t('people.profile.title')}</h2>
      <dl className="adm-rows">
        <Row
          label={t('people.field.displayName')}
          value={person.displayName}
          field={owner('displayName')}
        />
        <Row
          label={t('people.field.givenName')}
          value={person.givenName}
          field={owner('givenName')}
        />
        <Row
          label={t('people.field.familyName')}
          value={person.familyName}
          field={owner('familyName')}
        />
        <Row
          label={t('people.field.primaryEmail')}
          value={person.primaryEmail}
          field={owner('primaryEmail')}
        />
        {details ? (
          <>
            <Row
              label={t('people.field.employeeNumber')}
              value={person.employeeNumber}
              field={owner('employeeNumber')}
            />
            <Row
              label={t('people.field.manager')}
              value={
                managerId ? (
                  <Link to={`/admin/users/${encodeURIComponent(managerId)}`}>
                    {manager.data?.displayName ?? t('people.unknownPerson')}
                  </Link>
                ) : null
              }
              field={owner('manager')}
            />
            <Row
              label={t('people.field.department')}
              value={person.departmentId ? lookups.departmentName(person.departmentId) : null}
              field={owner('department')}
            />
            <Row
              label={t('people.field.primaryLocation')}
              value={
                person.primaryLocationId ? lookups.locationName(person.primaryLocationId) : null
              }
              field={owner('primaryLocation')}
            />
          </>
        ) : null}
        <Row
          label={t('people.field.status')}
          value={<PersonStatusBadge status={person.status} />}
          field={owner('status')}
        />
        {person.accessExpiresAt ? (
          <Row
            label={t('people.field.accessExpiresAt')}
            value={
              <time dateTime={person.accessExpiresAt}>
                {new Intl.DateTimeFormat(locale, { dateStyle: 'medium' }).format(
                  new Date(person.accessExpiresAt),
                )}
              </time>
            }
          />
        ) : null}
      </dl>
    </section>
  );
}

export function SignInCard({ person }: { person: PersonDetail }) {
  const { t } = useI18n();
  return (
    <section className="adm-card" aria-label={t('people.signin.title')}>
      <h2>{t('people.signin.title')}</h2>
      <dl className="adm-rows">
        <Row label={t('people.signin.method')} value={t(`people.signin.method.${person.source}`)} />
        <Row
          label={t('people.signin.statusSource')}
          value={t(
            person.statusSource === 'directory' ? 'people.owner.directory' : 'people.owner.turaco',
          )}
        />
        {person.externalIdentities.map((identity) => (
          <Row
            key={`${identity.providerKey}:${identity.username ?? ''}`}
            label={t('people.signin.identity', { provider: identity.providerKey })}
            value={
              <>
                {identity.username ?? '–'}{' '}
                {!identity.enabled ? (
                  <span className="adm-chip">{t('people.signin.disabledInDirectory')}</span>
                ) : null}
                {identity.lastSeenAt ? (
                  <span className="adm-sub">
                    {t('people.owner.seen')} <TableDate value={identity.lastSeenAt} />
                  </span>
                ) : null}
              </>
            }
          />
        ))}
      </dl>
      {person.source === 'local' ? (
        <p className="field-hint">{t('people.signin.localHint')}</p>
      ) : null}
      {person.source === 'emergency' ? (
        <Alert kind="info">{t('people.emergency.hint')}</Alert>
      ) : null}
    </section>
  );
}

/** Profile, department, location and manager in one dialog; only changed attributes are sent. */
export function EditProfileDialog({
  person,
  lookups,
  onClose,
  onSaved,
}: {
  person: PersonDetail;
  lookups: Lookups;
  onClose: () => void;
  onSaved: () => void;
}) {
  const { t } = useI18n();
  const { can } = useSession();
  const details = can('organization.users.view_details');
  const locked = new Set(
    person.fields.filter((field) => field.owner === 'directory').map((field) => field.key),
  );
  const [form, setForm] = useState({
    displayName: person.displayName,
    givenName: person.givenName ?? '',
    familyName: person.familyName ?? '',
    primaryEmail: person.primaryEmail ?? '',
    employeeNumber: person.employeeNumber ?? '',
  });
  const [departmentId, setDepartmentId] = useState(person.departmentId ?? '');
  const [locationId, setLocationId] = useState(person.primaryLocationId ?? '');
  const managerLabel = useAsync(
    (signal) =>
      person.managerUserId && details
        ? peopleAdminApi.user(person.managerUserId, signal).catch(() => undefined)
        : Promise.resolve(undefined),
    [person.managerUserId, details],
  );
  const [manager, setManager] = useState<PickedPerson | null | undefined>(undefined);
  const [touched, setTouched] = useState(false);
  // Remembers which parts went through when a later part needs a second attempt (for example a confirmation).
  const progress = useRef<{ version: number; done: Set<string> }>({
    version: person.version,
    done: new Set(),
  });

  const profile = profileChanges({ ...person, version: person.version }, form, locked);
  const departmentChanged =
    details &&
    departmentId !== (person.departmentId ?? '') &&
    !isFieldLocked(person.fields, 'department');
  const locationChanged =
    details &&
    locationId !== (person.primaryLocationId ?? '') &&
    !isFieldLocked(person.fields, 'primaryLocation');
  const currentManager =
    manager === undefined ? (person.managerUserId ?? null) : (manager?.id ?? null);
  const managerChanged =
    details &&
    manager !== undefined &&
    currentManager !== (person.managerUserId ?? null) &&
    !isFieldLocked(person.fields, 'manager');
  const anyChange = profile !== null || departmentChanged || locationChanged || managerChanged;
  const emailChanged = profile?.primaryEmail !== undefined;
  const emailInvalid = form.primaryEmail.trim() !== '' && !looksLikeEmail(form.primaryEmail);
  const nameMissing = form.displayName.trim() === '';

  const lockedHint = (key: string) =>
    isFieldLocked(person.fields, key)
      ? t('people.edit.lockedHint', { source: ownerOf(person.fields, key).source })
      : undefined;
  const text = (
    key: keyof typeof form,
    label: string,
    extra: { type?: string; error?: string } = {},
  ) => (
    <TextField
      label={label}
      value={form[key]}
      disabled={locked.has(key)}
      hint={lockedHint(key)}
      maxLength={200}
      onChange={(event) => setForm({ ...form, [key]: event.target.value })}
      {...(extra.type ? { type: extra.type } : {})}
      error={extra.error}
    />
  );

  const run = async () => {
    setTouched(true);
    const state = progress.current;
    if (profile && !state.done.has('profile')) {
      const updated = await peopleAdminApi.updateProfile(person.id, {
        ...profile,
        expectedVersion: state.version,
      });
      state.version = updated.version;
      state.done.add('profile');
    }
    if (departmentChanged && !state.done.has('department')) {
      const updated = await peopleAdminApi.setDepartment(
        person.id,
        state.version,
        departmentId || null,
      );
      state.version = updated.version;
      state.done.add('department');
    }
    if (locationChanged && !state.done.has('location')) {
      const updated = await peopleAdminApi.setPrimaryLocation(
        person.id,
        state.version,
        locationId || null,
      );
      state.version = updated.version;
      state.done.add('location');
    }
    if (managerChanged && !state.done.has('manager')) {
      const updated: PersonRow = await peopleAdminApi.setManager(
        person.id,
        state.version,
        manager?.id ?? null,
      );
      state.version = updated.version;
      state.done.add('manager');
    }
  };

  return (
    <GuardedActionDialog
      title={t('people.edit.title', { name: person.displayName })}
      confirmLabel={t('action.save')}
      disabled={!anyChange || emailInvalid || nameMissing}
      run={run}
      onDone={onSaved}
      onClose={onClose}
    >
      {locked.size > 0 ? <Alert kind="info">{t('people.edit.directoryNotice')}</Alert> : null}
      {text(
        'displayName',
        t('people.field.displayName'),
        touched && nameMissing ? { error: t('roles.name.required') } : {},
      )}
      {text('givenName', t('people.field.givenName'))}
      {text('familyName', t('people.field.familyName'))}
      {text('primaryEmail', t('people.field.primaryEmail'), {
        type: 'email',
        ...(emailInvalid ? { error: t('people.error.emailInvalid') } : {}),
      })}
      {emailChanged && person.source === 'local' ? (
        <Alert kind="warning">{t('people.edit.emailWarning')}</Alert>
      ) : null}
      {details ? (
        <>
          {text('employeeNumber', t('people.field.employeeNumber'))}
          <Select
            label={t('people.field.department')}
            value={departmentId}
            disabled={isFieldLocked(person.fields, 'department')}
            onChange={(event) => setDepartmentId(event.target.value)}
            options={[
              { value: '', label: t('people.edit.none') },
              ...lookups.departments
                .filter((item) => item.active || item.id === person.departmentId)
                .map((item) => ({
                  value: item.id,
                  label: lookups.departmentName(item.id) ?? item.name,
                })),
            ]}
          />
          <Select
            label={t('people.field.primaryLocation')}
            value={locationId}
            disabled={isFieldLocked(person.fields, 'primaryLocation')}
            onChange={(event) => setLocationId(event.target.value)}
            options={[
              { value: '', label: t('people.edit.none') },
              ...lookups.locations
                .filter((item) => item.active || item.id === person.primaryLocationId)
                .map((item) => ({
                  value: item.id,
                  label: lookups.locationName(item.id) ?? item.name,
                })),
            ]}
          />
          <fieldset className="adm-manager">
            <legend>{t('people.field.manager')}</legend>
            {isFieldLocked(person.fields, 'manager') ? (
              <p className="field-hint">{lockedHint('manager')}</p>
            ) : (
              <>
                <p className="field-hint">
                  {manager === undefined
                    ? person.managerUserId
                      ? t('people.edit.managerCurrent', {
                          name: managerLabel.data?.displayName ?? t('people.unknownPerson'),
                        })
                      : t('people.edit.none')
                    : manager
                      ? t('people.edit.managerNew', { name: manager.label })
                      : t('people.edit.managerRemove')}
                </p>
                <PersonPicker
                  value={manager ?? null}
                  onChange={(picked) => setManager(picked ?? undefined)}
                  exclude={[person.id]}
                />
                {person.managerUserId && manager !== null ? (
                  <button type="button" className="btn-link" onClick={() => setManager(null)}>
                    {t('people.edit.managerClear')}
                  </button>
                ) : null}
              </>
            )}
          </fieldset>
        </>
      ) : null}
    </GuardedActionDialog>
  );
}
