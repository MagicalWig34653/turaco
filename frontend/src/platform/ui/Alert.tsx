import type { ReactNode } from 'react';

type AlertProps = {
  kind: 'error' | 'warning' | 'info' | 'success';
  children: ReactNode;
  className?: string;
};

/** Errors are announced assertively (role=alert); other kinds politely (role=status). */
export function Alert({ kind, children, className }: AlertProps) {
  return (
    <div
      className={['alert', `alert-${kind}`, className].filter(Boolean).join(' ')}
      role={kind === 'error' ? 'alert' : 'status'}
    >
      {children}
    </div>
  );
}

export function Badge({
  tone = 'neutral',
  children,
}: {
  tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'unknown';
  children: ReactNode;
}) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}
