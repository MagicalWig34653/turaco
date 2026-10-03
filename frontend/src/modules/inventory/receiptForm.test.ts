import { describe, expect, it } from 'vitest';
import { parseSerials } from './receiptForm';

describe('parseSerials', () => {
  it('reads one unit per line with an optional asset tag', () => {
    expect(parseSerials('SN-1\n SN-2 ; TAG-2 \n\n  \nSN-3;')).toEqual([
      { serialNumber: 'SN-1' },
      { serialNumber: 'SN-2', assetTag: 'TAG-2' },
      { serialNumber: 'SN-3' },
    ]);
  });

  it('handles Windows line endings and empty input', () => {
    expect(parseSerials('A\r\nB')).toEqual([{ serialNumber: 'A' }, { serialNumber: 'B' }]);
    expect(parseSerials('')).toEqual([]);
  });
});
