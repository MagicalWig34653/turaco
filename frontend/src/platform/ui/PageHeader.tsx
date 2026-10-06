import type { ReactNode } from 'react';

export function PageHeader({
  title,
  eyebrow,
  intro,
  actions,
}: {
  title: string;
  eyebrow?: string | undefined;
  intro?: string | undefined;
  actions?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div>
        {eyebrow ? <p className="page-eyebrow">{eyebrow}</p> : null}
        <h1>{title}</h1>
        {intro ? <p className="subtitle">{intro}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </div>
  );
}
