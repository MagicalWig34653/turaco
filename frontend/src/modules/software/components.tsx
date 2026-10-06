import { useId, useState, type FormEvent, type ReactNode } from 'react';
import type { ApiError } from '../../platform/api/client';
import { asApiError } from '../../platform/api/useAsync';
import { useI18n } from '../../platform/i18n/I18nProvider';
import type { MessageKey } from '../../platform/i18n/i18n';
import { useSession } from '../../platform/session/SessionProvider';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
import { Button } from '../../platform/ui/Button';
import { copyContextText } from '../../platform/ui/ContextMenu';
import { Dialog } from '../../platform/ui/Dialog';
import { Select } from '../../platform/ui/Field';
import { StatusBadge } from '../../platform/ui/Workspace';
import { packageTone, productTone, shortHash, versionTone } from './helpers';
import type { PackageStatus, ProductStatus, VersionStatus } from './types';

export function ProductStatusBadge({ status }: { status: ProductStatus }) {
  const { t } = useI18n();
  return (
    <StatusBadge tone={productTone(status)}>
      {t(`software.productStatus.${status}` as MessageKey)}
    </StatusBadge>
  );
}

export function VersionStatusBadge({ status }: { status: VersionStatus }) {
  const { t } = useI18n();
  return (
    <StatusBadge tone={versionTone(status)}>
      {t(`software.versionStatus.${status}` as MessageKey)}
    </StatusBadge>
  );
}

export function PackageStatusBadge({ status }: { status: PackageStatus }) {
  const { t } = useI18n();
  return (
    <StatusBadge tone={packageTone(status)} live={status === 'requested' || status === 'building'}>
      {t(`software.packageStatus.${status}` as MessageKey)}
    </StatusBadge>
  );
}

export function HashMismatchBadge() {
  const { t } = useI18n();
  return <StatusBadge tone="danger">{t('software.package.hashMismatch')}</StatusBadge>;
}

/** A hash in a monospace chip with a copy button; `short` abbreviates it in dense tables. */
export function HashChip({
  value,
  label,
  short,
}: {
  value: string;
  label: string;
  short?: boolean;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState<'ok' | 'failed' | undefined>();
  return (
    <span className={`software-hash${short ? ' software-hash-short' : ''}`}>
      <code title={value} aria-label={`${label}: ${value}`}>
        {short ? shortHash(value) : value}
      </code>
      <button
        type="button"
        className="software-hash-copy"
        aria-label={t('software.hash.copy', { label })}
        onClick={() => void copyContextText(value).then((ok) => setCopied(ok ? 'ok' : 'failed'))}
      >
        <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false">
          <path d="M9 9h10v12H9zM5 15H4V3h11v1" />
        </svg>
      </button>
      <span className="software-hash-status" aria-live="polite">
        {copied === 'ok'
          ? t('software.hash.copied')
          : copied === 'failed'
            ? t('contextMenu.copyFallback')
            : ''}
      </span>
    </span>
  );
}

/** Who acted. User ids are not shown; the current user is recognized, a system actor is named. */
export function Actor({ userId, system }: { userId: string | null; system?: string | null }) {
  const { t } = useI18n();
  const { session } = useSession();
  if (system) return <>{t('software.actor.system')}</>;
  if (!userId) return <>—</>;
  return <>{userId === session?.userId ? t('software.actor.you') : t('software.actor.other')}</>;
}

/**
 * Confirms one operation. With `reasons` it requires a reason code (the API accepts codes only,
 * never free text); `reasonPrefix` translates them.
 */
export function OperationDialog({
  title,
  description,
  confirmLabel,
  danger,
  reasons,
  onSubmit,
  onClose,
}: {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  reasons?: readonly string[];
  onSubmit: (reason?: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const descriptionId = useId();
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await onSubmit(reasons ? reason : undefined);
    } catch (cause) {
      setError(asApiError(cause));
      setBusy(false);
    }
  };
  return (
    <Dialog title={title} onClose={onClose}>
      <form className="form" aria-describedby={descriptionId} onSubmit={(e) => void submit(e)}>
        <div className="dialog-body" id={descriptionId}>
          {description}
        </div>
        {reasons ? (
          <Select
            label={t('software.reason')}
            hint={t('software.reason.hint')}
            value={reason}
            required
            autoFocus
            onChange={(event) => setReason(event.target.value)}
            options={[
              { value: '', label: t('software.reason.choose') },
              ...reasons.map((value) => ({
                value,
                label: t(`software.reasonCode.${value}` as MessageKey),
              })),
            ]}
          />
        ) : null}
        {error ? <ApiErrorAlert error={error} /> : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={danger ? 'danger' : 'primary'}
            busy={busy}
            disabled={reasons !== undefined && reason === ''}
          >
            {confirmLabel}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
