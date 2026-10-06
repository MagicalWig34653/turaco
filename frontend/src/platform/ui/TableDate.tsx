import { useEffect, useState } from 'react';
import { useI18n } from '../i18n/I18nProvider';
import { formatDateTime } from '../format/format';
import { relativeDate } from './tableModel';

export function TableDate({ value }: { value: string | null | undefined }) {
  const { locale } = useI18n();
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 60000);
    return () => window.clearInterval(timer);
  }, []);
  if (!value) return <span>–</span>;
  const absolute = formatDateTime(locale, value);
  return (
    <time className="table-date" dateTime={value} title={absolute} aria-label={absolute}>
      {relativeDate(value, locale, now)}
    </time>
  );
}
