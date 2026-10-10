import { useState } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError, useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Link } from '../../platform/router/Router';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, Select, TextField } from '../../platform/ui/Field';
import { PageHeader } from '../../platform/ui/PageHeader';
import { peopleAdminApi } from './adminApi';
import { composeDisplayName, looksLikeEmail } from './adminModel';
import type { CredentialLink, PersonRow } from './adminTypes';
import { CredentialResult } from './OneTimeLink';
import { useOrgLookups } from './orgLookups';

type Step = 'who' | 'placement' | 'access' | 'review';
const steps: readonly Step[] = ['who', 'placement', 'access', 'review'];

type Outcome = {
  person: PersonRow;
  invitation?: CredentialLink;
  invitationError?: ApiError;
};

/** Wizard for a local User (no directory account): who, placement, how they sign in, review. */
export function UserCreateScreen() {
  const { t } = useI18n();
  const { can } = useSession();
  const details = can('organization.users.view_details');
  const lookups = useOrgLookups();
  const methods = useAsync(
    (signal) => peopleAdminApi.authMethods(signal).catch(() => undefined),
    [],
  );
  const [step, setStep] = useState<Step>('who');
  const [form, setForm] = useState({
    givenName: '',
    familyName: '',
    displayName: '',
    primaryEmail: '',
    employeeNumber: '',
    departmentId: '',
    locationId: '',
  });
  const [nameEdited, setNameEdited] = useState(false);
  const [invite, setInvite] = useState(true);
  const [touched, setTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);
  const [outcome, setOutcome] = useState<Outcome | null>(null);

  const update = (patch: Partial<typeof form>) => {
    const next = { ...form, ...patch };
    if (!nameEdited && ('givenName' in patch || 'familyName' in patch)) {
      next.displayName = composeDisplayName(next.givenName, next.familyName);
    }
    setForm(next);
  };
  const emailInvalid = form.primaryEmail.trim() !== '' && !looksLikeEmail(form.primaryEmail);
  const nameMissing = form.displayName.trim() === '';
  const whoValid = !nameMissing && !emailInvalid;
  const inviteNeedsEmail = invite && form.primaryEmail.trim() === '';

  const next = () => {
    if (step === 'who') {
      setTouched(true);
      if (!whoValid) return;
    }
    if (step === 'access' && inviteNeedsEmail) return;
    setStep(steps[Math.min(steps.indexOf(step) + 1, steps.length - 1)] ?? 'review');
  };
  const back = () => setStep(steps[Math.max(steps.indexOf(step) - 1, 0)] ?? 'who');

  const create = async () => {
    setBusy(true);
    setError(undefined);
    let person: PersonRow;
    try {
      person = await peopleAdminApi.createUser({
        displayName: form.displayName.trim(),
        ...(form.givenName.trim() ? { givenName: form.givenName.trim() } : {}),
        ...(form.familyName.trim() ? { familyName: form.familyName.trim() } : {}),
        ...(form.primaryEmail.trim() ? { primaryEmail: form.primaryEmail.trim() } : {}),
        ...(details && form.employeeNumber.trim()
          ? { employeeNumber: form.employeeNumber.trim() }
          : {}),
        ...(details && form.departmentId ? { departmentId: form.departmentId } : {}),
        ...(details && form.locationId ? { primaryLocationId: form.locationId } : {}),
      });
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
      return;
    }
    if (!invite) {
      setOutcome({ person });
      setBusy(false);
      return;
    }
    try {
      setOutcome({ person, invitation: await peopleAdminApi.sendInvitation(person.id) });
    } catch (cause) {
      setOutcome({ person, invitationError: asApiError(cause) });
    }
    setBusy(false);
  };

  const reset = () => {
    setForm({
      givenName: '',
      familyName: '',
      displayName: '',
      primaryEmail: '',
      employeeNumber: '',
      departmentId: '',
      locationId: '',
    });
    setNameEdited(false);
    setInvite(true);
    setTouched(false);
    setStep('who');
    setOutcome(null);
  };

  const retryInvitation = async () => {
    if (!outcome) return;
    setBusy(true);
    try {
      setOutcome({
        person: outcome.person,
        invitation: await peopleAdminApi.sendInvitation(outcome.person.id),
      });
    } catch (cause) {
      setOutcome({ person: outcome.person, invitationError: asApiError(cause) });
    }
    setBusy(false);
  };

  if (outcome) {
    const { person } = outcome;
    return (
      <>
        <PageHeader title={t('people.create.doneTitle')} eyebrow={t('sidebar.section.admin')} />
        <Alert kind="success">{t('people.create.done', { name: person.displayName })}</Alert>
        {outcome.invitation ? (
          <section className="adm-card" aria-label={t('credential.invitation.doneTitle')}>
            <h2>{t('credential.invitation.doneTitle')}</h2>
            <CredentialResult kind="invitation" result={outcome.invitation} person={person} />
          </section>
        ) : null}
        {outcome.invitationError ? (
          <>
            <Alert kind="warning">{t('people.create.invitationFailed')}</Alert>
            <ApiErrorAlert error={outcome.invitationError} />
            <div className="form-actions">
              <Button variant="primary" busy={busy} onClick={() => void retryInvitation()}>
                {t('people.create.retryInvitation')}
              </Button>
            </div>
          </>
        ) : null}
        {!invite ? <p className="field-hint">{t('people.create.noInvitation')}</p> : null}
        <div className="form-actions">
          <Link to={`/admin/users/${encodeURIComponent(person.id)}`} className="btn btn-primary">
            {t('people.create.open')}
          </Link>
          <Button onClick={reset}>{t('people.create.another')}</Button>
          <Link to="/admin/users" className="btn btn-secondary">
            {t('people.back')}
          </Link>
        </div>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title={t('people.create.title')}
        eyebrow={t('sidebar.section.admin')}
        intro={t('people.create.intro')}
        actions={
          <Link to="/admin/users" className="btn btn-secondary">
            {t('people.back')}
          </Link>
        }
      />
      <ol className="adm-steps" aria-label={t('people.create.steps')}>
        {steps.map((entry, index) => (
          <li
            key={entry}
            aria-current={entry === step ? 'step' : undefined}
            className={entry === step ? 'is-current' : index < steps.indexOf(step) ? 'is-done' : ''}
          >
            <span className="adm-step-number" aria-hidden="true">
              {index + 1}
            </span>
            {t(`people.create.step.${entry}`)}
          </li>
        ))}
      </ol>
      <form
        className="form adm-wide-form"
        onSubmit={(event) => {
          event.preventDefault();
          if (step === 'review') void create();
          else next();
        }}
        noValidate
      >
        {error ? <ApiErrorAlert error={error} /> : null}
        {step === 'who' ? (
          <>
            <TextField
              label={t('people.field.givenName')}
              value={form.givenName}
              maxLength={100}
              onChange={(event) => update({ givenName: event.target.value })}
            />
            <TextField
              label={t('people.field.familyName')}
              value={form.familyName}
              maxLength={100}
              onChange={(event) => update({ familyName: event.target.value })}
            />
            <TextField
              label={t('people.field.displayName')}
              hint={t('people.create.displayNameHint')}
              value={form.displayName}
              maxLength={200}
              required
              error={touched && nameMissing ? t('roles.name.required') : undefined}
              onChange={(event) => {
                setNameEdited(true);
                update({ displayName: event.target.value });
              }}
            />
            <TextField
              label={t('people.field.primaryEmail')}
              hint={t('people.create.emailHint')}
              type="email"
              value={form.primaryEmail}
              maxLength={254}
              error={emailInvalid ? t('people.error.emailInvalid') : undefined}
              onChange={(event) => update({ primaryEmail: event.target.value })}
            />
            {details ? (
              <TextField
                label={t('people.field.employeeNumber')}
                value={form.employeeNumber}
                maxLength={50}
                onChange={(event) => update({ employeeNumber: event.target.value })}
              />
            ) : null}
          </>
        ) : null}
        {step === 'placement' ? (
          details ? (
            <>
              <Select
                label={t('people.field.department')}
                value={form.departmentId}
                onChange={(event) => update({ departmentId: event.target.value })}
                options={[
                  { value: '', label: t('people.edit.none') },
                  ...lookups.departments
                    .filter((item) => item.active)
                    .map((item) => ({
                      value: item.id,
                      label: lookups.departmentName(item.id) ?? item.name,
                    })),
                ]}
              />
              <Select
                label={t('people.field.primaryLocation')}
                value={form.locationId}
                onChange={(event) => update({ locationId: event.target.value })}
                options={[
                  { value: '', label: t('people.edit.none') },
                  ...lookups.locations
                    .filter((item) => item.active)
                    .map((item) => ({
                      value: item.id,
                      label: lookups.locationName(item.id) ?? item.name,
                    })),
                ]}
              />
              <p className="field-hint">{t('people.create.placementHint')}</p>
            </>
          ) : (
            <Alert kind="info">{t('people.create.placementSkipped')}</Alert>
          )
        ) : null}
        {step === 'access' ? (
          <>
            <Alert kind="info">{t('people.create.accessIntro')}</Alert>
            {methods.data && !methods.data.local ? (
              <Alert kind="warning">{t('people.create.localDisabled')}</Alert>
            ) : null}
            <Checkbox
              label={t('people.create.invite')}
              description={t('people.create.inviteHint')}
              checked={invite}
              onChange={(event) => setInvite(event.target.checked)}
            />
            {inviteNeedsEmail ? (
              <Alert kind="warning">{t('people.create.inviteNeedsEmail')}</Alert>
            ) : null}
            <p className="field-hint">{t('people.create.rolesLater')}</p>
          </>
        ) : null}
        {step === 'review' ? (
          <section className="adm-card" aria-label={t('people.create.step.review')}>
            <dl className="adm-rows">
              <div className="adm-row">
                <dt>{t('people.field.displayName')}</dt>
                <dd>{form.displayName}</dd>
              </div>
              <div className="adm-row">
                <dt>{t('people.field.primaryEmail')}</dt>
                <dd>{form.primaryEmail || '–'}</dd>
              </div>
              {details ? (
                <>
                  <div className="adm-row">
                    <dt>{t('people.field.department')}</dt>
                    <dd>{lookups.departmentName(form.departmentId) ?? '–'}</dd>
                  </div>
                  <div className="adm-row">
                    <dt>{t('people.field.primaryLocation')}</dt>
                    <dd>{lookups.locationName(form.locationId) ?? '–'}</dd>
                  </div>
                </>
              ) : null}
              <div className="adm-row">
                <dt>{t('people.signin.method')}</dt>
                <dd>{t('people.signin.method.local')}</dd>
              </div>
              <div className="adm-row">
                <dt>{t('people.create.invite')}</dt>
                <dd>{invite ? t('common.yes') : t('common.no')}</dd>
              </div>
            </dl>
          </section>
        ) : null}
        <div className="form-actions">
          {step !== 'who' ? <Button onClick={back}>{t('action.back')}</Button> : null}
          {step === 'review' ? (
            <Button type="submit" variant="primary" busy={busy}>
              {t('people.create.submit')}
            </Button>
          ) : (
            <Button
              type="submit"
              variant="primary"
              disabled={step === 'access' && inviteNeedsEmail}
            >
              {t('action.next')}
            </Button>
          )}
        </div>
      </form>
    </>
  );
}
