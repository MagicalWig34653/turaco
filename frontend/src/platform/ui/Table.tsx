import type { TableHTMLAttributes } from 'react';

/** The themed frame for specialist tables whose rows contain screen-specific controls. */
export function Table({
  className = '',
  children,
  ...props
}: TableHTMLAttributes<HTMLTableElement>) {
  return (
    <div className="table-wrap">
      <table {...props} className={`table table-detail ${className}`.trim()}>
        {children}
      </table>
    </div>
  );
}
