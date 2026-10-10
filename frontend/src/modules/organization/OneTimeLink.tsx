import { useState } from 'react';
import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Alert } from '../../platform/ui/Alert';
import { Button } from '../../platform/ui/Button';
import { copyContextText } from '../../platform/ui/ContextMenu';
import type { CredentialLink } from './adminTypes';

/**
 * Shows the result of an invitation or reset. The link exists only in the answer to an invitation of a
 * never-activated account; it is kept in component state only and gone when this view closes.
 */
export function CredentialResult({
  kind,
  result,
  person,
}: {
  kind: 'invitation' | 'reset';
  result: CredentialLink;
  person: { displayName: string; primaryEmail?: string | null };
}) {
  const { t, locale } = useI18n();
  const [copied, setCopied] = useState<'idle' | 'done' | 'failed'>('idle');
  const copy = async () => {
    if (!result.link) return;
    setCopied((await copyContextText(result.link)) ? 'done' : 'failed');
  };
  return (
    <div className="adm-credential" data-testid="credential-result">
      {result.mailed ? (
        <Alert kind="success">
          {t(kind === 'invitation' ? 'credential.mailed.invitation' : 'credential.mailed.reset', {
            email: person.primaryEmail ?? '',
          })}
        </Alert>
      ) : result.link ? null : (
        <Alert kind="warning">{t('credential.notMailed')}</Alert>
      )}
      {result.link ? (
        <>
          <Alert kind="warning">
            <p>
              <strong>{t('credential.link.onceTitle')}</strong>
            </p>
            <p>{t('credential.link.onceBody', { name: person.displayName })}</p>
          </Alert>
          <div className="field">
            <label htmlFor="credential-link">{t('credential.link.label')}</label>
            <div className="adm-copy-row">
              <input
                id="credential-link"
                type="text"
                readOnly
                value={result.link}
                onFocus={(event) => event.currentTarget.select()}
                autoComplete="off"
                spellCheck={false}
              />
              <Button variant="primary" onClick={() => void copy()}>
                {t('credential.link.copy')}
              </Button>
            </div>
            <p className="field-hint" role="status">
              {copied === 'done'
                ? t('credential.link.copied')
                : copied === 'failed'
                  ? t('credential.link.copyFailed')
                  : t('credential.link.valid', { date: formatDateTime(locale, result.expiresAt) })}
            </p>
          </div>
        </>
      ) : (
        <p className="field-hint">
          {t('credential.valid', { date: formatDateTime(locale, result.expiresAt) })}
        </p>
      )}
    </div>
  );
}
