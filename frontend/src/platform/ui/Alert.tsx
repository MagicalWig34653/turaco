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

/** `live` marks an ongoing state (for example in progress); themes may animate its dot. */
export function Badge({
  tone = 'neutral',
  live = false,
  children,
}: {
  tone?: 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'unknown';
  live?: boolean;
  children: ReactNode;
}) {
  return (
    <span className={`badge badge-${tone}${live ? ' badge-live' : ''}`}>
      <span className="badge-icon" aria-hidden="true">
        {{ neutral: '•', success: '✓', warning: '!', danger: '!', info: '•', unknown: '?' }[tone]}
      </span>
      {children}
    </span>
  );
}
