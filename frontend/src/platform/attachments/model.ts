export type AttachmentConfig = {
  enabled: boolean;
  maxBytes: number;
  allowedTypes: string[];
  maxPerOwner?: number;
};

export type ScanStatus = 'pending' | 'clean' | 'infected' | 'failed';
export type AttachmentAudience = 'all' | 'privileged';

export type Attachment = {
  id: string;
  ownerType: string;
  ownerId: string;
  fileName: string;
  contentType: string;
  sizeBytes: number;
  sha256: string;
  scanStatus: ScanStatus;
  audience: AttachmentAudience;
  uploadedBy: string;
  createdAt: string;
  scannedAt: string | null;
  downloadable: boolean;
};

export type FileCheck = 'tooLarge' | 'unsupportedType' | 'empty' | 'limit';

/**
 * Client-side pre-check against the server config. The server stays authoritative (it also checks magic bytes), so
 * an unknown browser type (empty) is let through instead of being rejected here.
 */
export function checkFile(
  file: { size: number; type: string },
  config: AttachmentConfig,
  currentCount: number,
): FileCheck | undefined {
  if (config.maxPerOwner !== undefined && currentCount >= config.maxPerOwner) return 'limit';
  if (file.size <= 0) return 'empty';
  if (file.size > config.maxBytes) return 'tooLarge';
  if (file.type !== '' && !config.allowedTypes.includes(file.type.toLowerCase())) {
    return 'unsupportedType';
  }
  return undefined;
}

/** Human readable size with binary units and one decimal for KB and above. */
export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '–';
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  const units = ['KB', 'MB', 'GB'];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

/** Warning/danger are used only as status colors for scan results, never brand colors. */
export function scanTone(status: ScanStatus): 'neutral' | 'success' | 'danger' | 'warning' {
  switch (status) {
    case 'clean':
      return 'success';
    case 'infected':
      return 'danger';
    case 'failed':
      return 'warning';
    default:
      return 'neutral';
  }
}

/** Download is offered only when the server says so (clean scan); the status is never guessed from the name. */
export function canDownload(a: Pick<Attachment, 'downloadable'>): boolean {
  return a.downloadable;
}

/** The audience choice is offered to staff only; everybody else uploads with the default audience. */
export function uploadAudience(
  staff: boolean,
  choice: AttachmentAudience,
): AttachmentAudience | undefined {
  return staff && choice === 'privileged' ? 'privileged' : undefined;
}
