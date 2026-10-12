import { formatDateTime } from '../../platform/format/format';
import { useI18n } from '../../platform/i18n/I18nProvider';
import { Badge } from '../../platform/ui/Alert';
import { relativeDue, slaTone, type SlaSummary } from './slaModel';

/**
 * The "Response due" / "Resolution due" cell of the Ticket list: relative time with the exact time as tooltip and a
 * chip for the derived SLA state. Staff only: the summary is absent for requesters and while the switch is off.
 */
export function SlaDue({
  summary,
  field,
}: {
  summary: SlaSummary | undefined;
  field: 'responseDueAt' | 'resolutionDueAt';
}) {
  const { t, locale } = useI18n();
  if (!summary) return <>–</>;
  const iso = summary[field];
  const due = relativeDue(iso, new Date());
  if (!iso || !due) {
    return summary.state === 'none' ? (
      <>–</>
    ) : (
      <Badge tone={slaTone(summary.state)}>{t(`sla.state.${summary.state}`)}</Badge>
    );
  }
  return (
    <span title={formatDateTime(locale, iso)}>
      {t(`sla.relative.${due.unit}${due.past ? 'Ago' : ''}`, { count: due.count })}{' '}
      <Badge tone={slaTone(summary.state)}>{t(`sla.state.${summary.state}`)}</Badge>
    </span>
  );
}
