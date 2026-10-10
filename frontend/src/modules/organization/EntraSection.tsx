import { useState } from 'react';
import { useAsync } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useSession } from '../../platform/session/SessionProvider';
import { Alert } from '../../platform/ui/Alert';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { Checkbox, TextField } from '../../platform/ui/Field';
import { GuardedActionDialog } from '../../platform/ui/GuardedActionDialog';
import { TableDate } from '../../platform/ui/TableDate';
import { peopleAdminApi } from './adminApi';
import type { EntraLinkResult, ExternalIdentity, PersonDetail } from './adminTypes';
import {
  entraActions,
  entraFormErrors,
  entraTenantOf,
  isEntraIdentity,
  normalizeGuid,
} from './entraModel';

const viaLabel: Record<string, MessageKey> = {
  administrator: 'entra.via.administrator',
  source_anchor: 'entra.via.sourceAnchor',
  provisioning: 'entra.via.provisioning',
  cli: 'entra.via.cli',
};

/**
 * Microsoft Entra sign-in of one User: the linked identities (tenant, last four characters of the object id, when
 * and how it was linked) and, for platform administrators, linking and removing. Linking lets whoever controls the
 * Entra account sign in as this person, so it asks for an explicit confirmation; the server enforces who may do it.
 */
export function EntraSection({
  person,
  onChanged,
}: {
  person: PersonDetail;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const { can, session } = useSession();
  const identities = person.externalIdentities.filter(isEntraIdentity);
  const actions = entraActions(person, can, session?.userId);
  const [dialog, setDialog] = useState<'link' | ExternalIdentity | null>(null);
  const [notice, setNotice] = useState<EntraLinkResult | null>(null);
  if (identities.length === 0 && !actions.link) return null;

  const done = (result?: EntraLinkResult) => {
    setNotice(result ?? null);
    setDialog(null);
    onChanged();
  };

  return (
    <section className="adm-card" aria-label={t('entra.title')}>
      <h2>{t('entra.title')}</h2>
      {notice ? (
        <Alert kind="success">
          {notice.noticeSent ? t('entra.notice.sent') : t('entra.notice.notSent')}
        </Alert>
      ) : null}
      {identities.length === 0 ? <p className="field-hint">{t('entra.empty')}</p> : null}
      <dl className="adm-rows">
        {identities.map((identity) => (
          <div key={identity.id} className="adm-row">
            <dt>{t('entra.identity.tenant', { tenant: entraTenantOf(identity) })}</dt>
            <dd>
              {t('entra.identity.object', { suffix: identity.subjectSuffix })}{' '}
              {identity.via ? (
                <span className="adm-chip">
                  {t(viaLabel[identity.via] ?? 'entra.via.administrator')}
                </span>
              ) : null}
              <span className="adm-sub">
                {t('entra.identity.linkedAt')} <TableDate value={identity.linkedAt} />
              </span>
              {actions.unlink ? (
                <Button variant="secondary" onClick={() => setDialog(identity)}>
                  {t('entra.unlink.action')}
                </Button>
              ) : null}
            </dd>
          </div>
        ))}
      </dl>
      {actions.link ? (
        <Button variant="secondary" onClick={() => setDialog('link')}>
          {t('entra.link.action')}
        </Button>
      ) : null}
      {dialog === 'link' ? (
        <LinkDialog person={person} onDone={done} onClose={() => setDialog(null)} />
      ) : null}
      {dialog && dialog !== 'link' ? (
        <UnlinkDialog
          person={person}
          identity={dialog}
          onDone={done}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </section>
  );
}

function LinkDialog({
  person,
  onDone,
  onClose,
}: {
  person: PersonDetail;
  onDone: (result: EntraLinkResult) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const info = useAsync((signal) => peopleAdminApi.entraLinking(signal), []);
  const [tenant, setTenant] = useState<string | null>(null);
  const [objectId, setObjectId] = useState('');
  const [understood, setUnderstood] = useState(false);
  const [touched, setTouched] = useState(false);
  let result: EntraLinkResult | undefined;
  // The tenant defaults to the installation's home tenant until the administrator types another one.
  const tenantValue = tenant ?? info.data?.tenantId ?? '';
  const errors = entraFormErrors(tenantValue, objectId);
  const invalid = Object.keys(errors).length > 0;
  const message = (field: 'tenant' | 'object', kind: 'required' | 'invalid' | undefined) =>
    touched && kind
      ? t(kind === 'required' ? 'entra.link.required' : `entra.link.${field}Invalid`)
      : undefined;
  return (
    <GuardedActionDialog
      title={t('entra.link.title', { name: person.displayName })}
      confirmLabel={t('entra.link.confirm')}
      danger
      wide
      disabled={!info.data?.configured || invalid || !understood}
      run={async () => {
        result = await peopleAdminApi.linkEntraIdentity(
          person.id,
          person.version,
          normalizeGuid(tenantValue),
          normalizeGuid(objectId),
        );
      }}
      onDone={() => {
        if (result) onDone(result);
      }}
      onClose={onClose}
    >
      <p>{t('entra.link.intro')}</p>
      {info.error ? (
        <ApiErrorAlert error={info.error} onRetry={info.reload} />
      ) : info.loading ? (
        <p role="status">{t('state.loading')}</p>
      ) : info.data && !info.data.configured ? (
        <Alert kind="warning">{t('entra.link.notConfigured')}</Alert>
      ) : (
        <>
          <TextField
            label={t('entra.link.tenant')}
            hint={t('entra.link.tenantHint')}
            value={tenantValue}
            onChange={(event) => setTenant(event.target.value)}
            onBlur={() => setTouched(true)}
            error={message('tenant', errors.tenant)}
            autoComplete="off"
            spellCheck={false}
            required
          />
          <TextField
            label={t('entra.link.object')}
            hint={t('entra.link.objectHint')}
            value={objectId}
            onChange={(event) => setObjectId(event.target.value)}
            onBlur={() => setTouched(true)}
            error={message('object', errors.object)}
            autoComplete="off"
            spellCheck={false}
            required
          />
        </>
      )}
      <Alert kind="warning">
        <p>
          <strong>{t('entra.link.takeover')}</strong>
        </p>
        <ul className="adm-impact-list">
          <li>{t('entra.link.effect.signin')}</li>
          <li>{t('entra.link.effect.password')}</li>
          <li>{t('entra.link.effect.roles')}</li>
          <li>{t('entra.link.effect.notice')}</li>
        </ul>
      </Alert>
      <Checkbox
        label={t('entra.link.understood', { name: person.displayName })}
        checked={understood}
        onChange={(event) => setUnderstood(event.target.checked)}
      />
    </GuardedActionDialog>
  );
}

function UnlinkDialog({
  person,
  identity,
  onDone,
  onClose,
}: {
  person: PersonDetail;
  identity: ExternalIdentity;
  onDone: (result: EntraLinkResult) => void;
  onClose: () => void;
}) {
  const { t } = useI18n();
  let result: EntraLinkResult | undefined;
  return (
    <GuardedActionDialog
      title={t('entra.unlink.title', { name: person.displayName })}
      confirmLabel={t('entra.unlink.confirm')}
      danger
      run={async () => {
        result = await peopleAdminApi.unlinkExternalIdentity(person.id, identity.id);
      }}
      onDone={() => {
        if (result) onDone(result);
      }}
      onClose={onClose}
    >
      <p>
        {t('entra.unlink.body', {
          tenant: entraTenantOf(identity),
          suffix: identity.subjectSuffix,
        })}
      </p>
      <Alert kind="warning">{t('entra.unlink.effect')}</Alert>
    </GuardedActionDialog>
  );
}
