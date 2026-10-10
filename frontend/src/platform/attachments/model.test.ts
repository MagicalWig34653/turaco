import { describe, expect, it } from 'vitest';
import {
  canDownload,
  checkFile,
  formatBytes,
  scanTone,
  uploadAudience,
  type AttachmentConfig,
} from './model';

const config: AttachmentConfig = {
  enabled: true,
  maxBytes: 1000,
  allowedTypes: ['application/pdf', 'image/png'],
  maxPerOwner: 2,
};

describe('checkFile', () => {
  it('accepts an allowed file', () => {
    expect(checkFile({ size: 10, type: 'application/pdf' }, config, 0)).toBeUndefined();
  });
  it('rejects too large, empty, wrong type and a full owner', () => {
    expect(checkFile({ size: 1001, type: 'image/png' }, config, 0)).toBe('tooLarge');
    expect(checkFile({ size: 0, type: 'image/png' }, config, 0)).toBe('empty');
    expect(checkFile({ size: 5, type: 'text/html' }, config, 0)).toBe('unsupportedType');
    expect(checkFile({ size: 5, type: 'image/png' }, config, 2)).toBe('limit');
  });
  it('leaves an unknown browser type to the server', () => {
    expect(checkFile({ size: 5, type: '' }, config, 0)).toBeUndefined();
  });
});

describe('formatBytes', () => {
  it('uses binary units', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MB');
    expect(formatBytes(-1)).toBe('–');
  });
});

describe('scan state', () => {
  it('maps status to a semantic tone', () => {
    expect(scanTone('clean')).toBe('success');
    expect(scanTone('infected')).toBe('danger');
    expect(scanTone('failed')).toBe('warning');
    expect(scanTone('pending')).toBe('neutral');
  });
  it('downloads only what the server marks downloadable', () => {
    expect(canDownload({ downloadable: true })).toBe(true);
    expect(canDownload({ downloadable: false })).toBe(false);
  });
});

describe('uploadAudience', () => {
  it('sends privileged only for staff who chose it', () => {
    expect(uploadAudience(true, 'privileged')).toBe('privileged');
    expect(uploadAudience(false, 'privileged')).toBeUndefined();
    expect(uploadAudience(true, 'all')).toBeUndefined();
  });
});
