import type { FormField } from './types';

/** Values of the form while it is edited: booleans for checkboxes, strings for everything else. */
export type FormValues = Record<string, string | boolean>;

/** Codes shared with the server's validation (catalog answer validation). */
export type FieldErrorCode =
  | 'required'
  | 'invalid_type'
  | 'too_long'
  | 'out_of_range'
  | 'invalid_choice'
  | 'unknown_field'
  | 'invalid_date'
  | 'invalid_characters'
  | 'not_allowed'
  | 'not_active'
  | 'not_found';

export function initialValues(fields: readonly FormField[]): FormValues {
  const values: FormValues = {};
  for (const field of fields) values[field.key] = field.type === 'boolean' ? false : '';
  return values;
}

/**
 * Converts the edited values into the answers payload and reports client-side problems per field.
 * Empty optional answers are omitted; the server validates everything again and stays authoritative.
 */
export function toAnswers(
  fields: readonly FormField[],
  values: FormValues,
): { answers: Record<string, unknown>; errors: Record<string, FieldErrorCode> } {
  const answers: Record<string, unknown> = {};
  const errors: Record<string, FieldErrorCode> = {};
  for (const field of fields) {
    const raw = values[field.key];
    if (field.type === 'boolean') {
      // An unchecked required checkbox is a "no" answer, never missing; only true or false is sent.
      answers[field.key] = raw === true;
      continue;
    }
    const text = typeof raw === 'string' ? raw.trim() : '';
    if (text === '') {
      if (field.required) errors[field.key] = 'required';
      continue;
    }
    if (field.type === 'number') {
      if (!/^-?\d+$/.test(text)) {
        errors[field.key] = 'invalid_type';
        continue;
      }
      const n = Number.parseInt(text, 10);
      if (
        (field.min !== undefined && n < field.min) ||
        (field.max !== undefined && n > field.max)
      ) {
        errors[field.key] = 'out_of_range';
        continue;
      }
      answers[field.key] = n;
      continue;
    }
    if (
      (field.type === 'text' || field.type === 'longtext') &&
      field.maxLength !== undefined &&
      [...text].length > field.maxLength
    ) {
      errors[field.key] = 'too_long';
      continue;
    }
    answers[field.key] = text;
  }
  return { answers, errors };
}

const known = new Set<string>([
  'required',
  'invalid_type',
  'too_long',
  'out_of_range',
  'invalid_choice',
  'unknown_field',
  'invalid_date',
  'invalid_characters',
  'not_allowed',
  'not_active',
  'not_found',
]);

/** Message key of a field error code; unknown codes fall back to the generic text. */
export function fieldErrorKey(code: string): `requests.fieldError.${FieldErrorCode | 'generic'}` {
  return known.has(code)
    ? (`requests.fieldError.${code as FieldErrorCode}` as const)
    : 'requests.fieldError.generic';
}
