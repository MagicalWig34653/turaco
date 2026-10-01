import type { ReactNode } from 'react';

export function PageHeader({
  title,
  intro,
  actions,
}: {
  title: string;
  intro?: string | undefined;
  actions?: ReactNode;
}) {
  return (
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {intro ? <p className="subtitle">{intro}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
    </div>
  );
}
