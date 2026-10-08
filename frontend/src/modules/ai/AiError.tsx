import { asApiError } from '../../platform/api/useAsync';
import { ApiErrorAlert } from '../../platform/ui/ApiErrorAlert';
export function AiError({ error }: { error: unknown }) {
  return error ? <ApiErrorAlert error={asApiError(error)} /> : null;
}
