import type { MessageKey } from '../../platform/i18n/i18n';
import { impactChoices, impactLabelKey, type ImpactChoice } from './reportModel';

/**
 * Message key of the impact the reporter chose, for the requester card. A patient-care flag without a
 * known impact choice (older tickets) still reads as patient care.
 */
export function reportedImpactKey(
  impact: string | null | undefined,
  patientImpact: boolean | undefined,
): MessageKey | null {
  if (impact && (impactChoices as readonly string[]).includes(impact))
    return impactLabelKey[impact as ImpactChoice];
  return patientImpact ? impactLabelKey.patient_care : null;
}
