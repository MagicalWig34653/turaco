/** The colleague lookup refuses text shorter than this (query.query_too_short). */
export const lookupMinChars = 3;

export function lookupState(text: string): { search: boolean; tooShort: boolean } {
  const length = [...text.trim()].length;
  return { search: length >= lookupMinChars, tooShort: length > 0 && length < lookupMinChars };
}
