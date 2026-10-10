import { useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import type { ApiError } from '../api/client';
import { needsImpactConfirmation, needsRuleAcknowledgement, parseConflict } from '../api/conflict';
import { asApiError } from '../api/useAsync';
import { useI18n } from '../i18n/I18nProvider';
import type { MessageKey } from '../i18n/i18n';
import { Alert } from './Alert';
import { ApiErrorAlert } from './ApiErrorAlert';
import { Button } from './Button';
import { Dialog } from './Dialog';
import { Checkbox, TextArea } from './Field';

/** What the server asked for in addition to the first request. */
export type GuardExtras = { confirmImpact?: true; acknowledgedRules?: string[]; reason?: string };

type Step =
  | { kind: 'form' }
  | { kind: 'impact'; counts: Record<string, number> }
  | { kind: 'rules'; rules: string[] };

type Props = {
  title: string;
  confirmLabel: string;
  danger?: boolean;
  /** The form or summary shown before the first request. */
  children?: ReactNode;
  disabled?: boolean;
  /** Runs the operation; a 409 with impact counts or SoD rules moves the dialog to a confirmation step. */
  run: (extras: GuardExtras) => Promise<void>;
  onDone: () => void;
  onClose: () => void;
  wide?: boolean;
};

/**
 * One dialog for operations the server may ask to confirm: it sends the request, and when the answer is
 * `organization.impact_confirmation_required` (details.counts) or a separation-of-duties conflict
 * (details.rules) it shows the consequences and repeats the request with the confirmation. Any other
 * error is shown as localized text and the dialog stays open.
 */
export function GuardedActionDialog({
  title,
  confirmLabel,
  danger,
  children,
  disabled,
  run,
  onDone,
  onClose,
  wide,
}: Props) {
  const { t } = useI18n();
  const [step, setStep] = useState<Step>({ kind: 'form' });
  const [extras, setExtras] = useState<GuardExtras>({});
  const [reason, setReason] = useState('');
  const [understood, setUnderstood] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | undefined>(undefined);

  const optional = (key: string, fallback: string, params?: Record<string, string | number>) => {
    const text = t(key as MessageKey, params);
    return text === key ? fallback : text;
  };

  const attempt = async (next: GuardExtras) => {
    setBusy(true);
    setError(undefined);
    try {
      await run(next);
      onDone();
    } catch (cause) {
      const failure = asApiError(cause);
      if (needsImpactConfirmation(failure) && !next.confirmImpact) {
        setExtras(next);
        setStep({ kind: 'impact', counts: parseConflict(failure).counts });
      } else if (needsRuleAcknowledgement(failure) && !next.acknowledgedRules) {
        setExtras(next);
        setStep({ kind: 'rules', rules: parseConflict(failure).rules });
      } else setError(failure);
      setBusy(false);
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (step.kind === 'form') void attempt({});
    else if (step.kind === 'impact') void attempt({ ...extras, confirmImpact: true });
    else void attempt({ ...extras, acknowledgedRules: step.rules, reason: reason.trim() });
  };

  const blockedFields = error ? parseConflict(error).fields : [];
  const canSubmit =
    step.kind === 'form'
      ? !disabled
      : step.kind === 'impact'
        ? understood
        : understood && reason.trim().length >= 3;

  return (
    <Dialog title={title} onClose={onClose} wide={wide ?? true}>
      <form className="form adm-guarded" onSubmit={submit} noValidate>
        {error ? <ApiErrorAlert error={error} /> : null}
        {blockedFields.length > 0 ? (
          <Alert kind="warning">
            <p>{t('guard.fieldsLocked')}</p>
            <ul>
              {blockedFields.map((field) => (
                <li key={field}>{optional(`people.field.${field}`, field)}</li>
              ))}
            </ul>
          </Alert>
        ) : null}
        {step.kind === 'form' ? children : null}
        {step.kind === 'impact' ? (
          <>
            <Alert kind="warning">
              <p>
                <strong>{t('guard.impact.title')}</strong>
              </p>
              <p>{t('guard.impact.intro')}</p>
              <ul className="adm-impact-list">
                {Object.entries(step.counts).map(([key, count]) => (
                  <li key={key}>
                    {optional(`guard.impact.count.${key}`, `${key}: ${count}`, { count })}
                  </li>
                ))}
                {Object.keys(step.counts).length === 0 ? (
                  <li>{t('guard.impact.unknown')}</li>
                ) : null}
              </ul>
            </Alert>
            <Checkbox
              label={t('guard.impact.confirm')}
              checked={understood}
              onChange={(event) => setUnderstood(event.target.checked)}
            />
          </>
        ) : null}
        {step.kind === 'rules' ? (
          <>
            <Alert kind="warning">
              <p>
                <strong>{t('guard.rules.title')}</strong>
              </p>
              <p>{t('guard.rules.intro')}</p>
              <ul className="adm-impact-list">
                {step.rules.map((rule) => (
                  <li key={rule}>{optional(`roles.sod.${rule}`, rule)}</li>
                ))}
              </ul>
            </Alert>
            <TextArea
              label={t('guard.rules.reason')}
              hint={t('guard.rules.reasonHint')}
              value={reason}
              onChange={(event) => setReason(event.target.value)}
              maxLength={500}
              rows={3}
              required
            />
            <Checkbox
              label={t('guard.rules.confirm')}
              checked={understood}
              onChange={(event) => setUnderstood(event.target.checked)}
            />
          </>
        ) : null}
        <div className="dialog-actions">
          <Button onClick={onClose}>{t('action.cancel')}</Button>
          <Button
            type="submit"
            variant={danger || step.kind !== 'form' ? 'danger' : 'primary'}
            busy={busy}
            disabled={!canSubmit}
          >
            {step.kind === 'form' ? confirmLabel : t('guard.continue')}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
