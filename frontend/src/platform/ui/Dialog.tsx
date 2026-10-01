import { useEffect, useId, useRef } from 'react';
import type { ReactNode } from 'react';
import { useI18n } from '../i18n/I18nProvider';
import { Alert } from './Alert';
import { Button } from './Button';

type DialogProps = {
  title: string;
  onClose: () => void;
  children: ReactNode;
  wide?: boolean;
};

/**
 * Modal built on the native <dialog>: focus moves into it on open, is trapped while open and returns
 * to the previously focused element on close; Escape closes. Mount it only while it is open.
 */
export function Dialog({ title, onClose, children, wide }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return undefined;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!dialog.open) dialog.showModal();
    return () => {
      if (dialog.open) dialog.close();
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  return (
    <dialog
      ref={ref}
      className={wide ? 'dialog dialog-wide' : 'dialog'}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        onCloseRef.current();
      }}
    >
      <h2 id={titleId}>{title}</h2>
      {children}
    </dialog>
  );
}

type ConfirmDialogProps = {
  title: string;
  message: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  busy?: boolean;
  /** Pre-rendered localized error text from a failed confirmation. */
  error?: ReactNode;
  onConfirm: () => void;
  onCancel: () => void;
};

export function ConfirmDialog({
  title,
  message,
  confirmLabel,
  danger,
  busy,
  error,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const { t } = useI18n();
  return (
    <Dialog title={title} onClose={onCancel}>
      <div className="dialog-body">{message}</div>
      {error ? <Alert kind="error">{error}</Alert> : null}
      <div className="dialog-actions">
        <Button onClick={onCancel} autoFocus>
          {t('action.cancel')}
        </Button>
        <Button variant={danger ? 'danger' : 'primary'} busy={busy ?? false} onClick={onConfirm}>
          {confirmLabel}
        </Button>
      </div>
    </Dialog>
  );
}
