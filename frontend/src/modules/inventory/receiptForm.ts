/**
 * Parses the serial number box of a goods receipt: one unit per line, "serial" or
 * "serial;asset tag". Blank lines are ignored.
 */
export function parseSerials(text: string): Array<{ serialNumber: string; assetTag?: string }> {
  const units: Array<{ serialNumber: string; assetTag?: string }> = [];
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (line === '') continue;
    const [serial = '', tag = ''] = line.split(';').map((part) => part.trim());
    units.push(tag === '' ? { serialNumber: serial } : { serialNumber: serial, assetTag: tag });
  }
  return units;
}
