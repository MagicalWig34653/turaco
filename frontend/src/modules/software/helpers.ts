import { isApiError } from '../../platform/api/client';
import type { MessageKey } from '../../platform/i18n/i18n';
import type {
  PackageStatus,
  ProductOperation,
  ProductStatus,
  SoftwarePackage,
  SoftwareVersion,
  VersionOperation,
  VersionStatus,
} from './types';

export type Tone = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'unknown';

/** The software lists send an empty nextCursor on the last page; the paged list expects none. */
export function normalizePage<T>(page: { items: T[]; nextCursor?: string | null }): {
  items: T[];
  nextCursor?: string;
} {
  return page.nextCursor
    ? { items: page.items, nextCursor: page.nextCursor }
    : { items: page.items };
}

export function productTone(status: ProductStatus): Tone {
  return (
    {
      candidate: 'info',
      approved: 'success',
      deprecated: 'warning',
      retired: 'neutral',
      blocked: 'danger',
    } as const
  )[status];
}

export function versionTone(status: VersionStatus): Tone {
  return (
    {
      registered: 'neutral',
      pending: 'warning',
      approved: 'success',
      rejected: 'danger',
      revoked: 'danger',
    } as const
  )[status];
}

export function packageTone(status: PackageStatus): Tone {
  return (
    {
      requested: 'info',
      building: 'info',
      packaged: 'neutral',
      published: 'success',
      failed: 'danger',
    } as const
  )[status];
}

/** Product operations the API allows from a status (docs/domain/state-machines.md). */
export function productOperations(status: ProductStatus): ProductOperation[] {
  const byStatus: Record<ProductStatus, ProductOperation[]> = {
    candidate: ['approve', 'block'],
    approved: ['deprecate', 'block'],
    deprecated: ['retire', 'block'],
    retired: ['block'],
    blocked: ['unblock'],
  };
  return byStatus[status];
}

export type StepState = 'done' | 'current' | 'upcoming' | 'failed';
export type ApprovalStep = { id: VersionStatus; state: StepState };

/**
 * The approval stepper: registered → pending → approved, where a rejection replaces the approved
 * step and a revocation follows it.
 */
export function approvalSteps(status: VersionStatus): ApprovalStep[] {
  const order: VersionStatus[] = ['registered', 'pending'];
  if (status === 'rejected') order.push('rejected');
  else order.push('approved');
  if (status === 'revoked') order.push('revoked');
  const index = order.indexOf(status);
  return order.map((id, i) => ({
    id,
    state:
      i < index
        ? 'done'
        : i > index
          ? 'upcoming'
          : id === 'rejected' || id === 'revoked'
            ? 'failed'
            : id === 'approved'
              ? 'done'
              : 'current',
  }));
}

export type VersionAction = {
  id: VersionOperation | 'package';
  /** Translation key explaining why the action is unavailable; absent when it can run. */
  disabledReason?: MessageKey;
};

/**
 * The version actions the current user may see. Hidden when the permission is missing or the
 * status never allows it; disabled with a reason when the server would refuse for this user.
 */
export function versionActions(
  version: Pick<SoftwareVersion, 'approvalStatus' | 'registeredBy' | 'requestedBy'>,
  userId: string | undefined,
  can: (permission: string) => boolean,
): VersionAction[] {
  const out: VersionAction[] = [];
  const status = version.approvalStatus;
  if (can('software.package') && status === 'registered') out.push({ id: 'request-approval' });
  if (can('software.approve') && status === 'pending') {
    const own =
      userId !== undefined && (version.registeredBy === userId || version.requestedBy === userId);
    out.push(
      own
        ? { id: 'approve', disabledReason: 'software.disabled.separationOfDuties' }
        : { id: 'approve' },
    );
    out.push({ id: 'reject' });
  }
  if (can('software.approve') && status === 'approved') out.push({ id: 'revoke' });
  if (can('software.package') && ['pending', 'approved'].includes(status)) {
    out.push(
      status === 'approved'
        ? { id: 'package' }
        : { id: 'package', disabledReason: 'software.disabled.versionNotApproved' },
    );
  }
  return out;
}

/** Why a package cannot be published, or undefined when publishing is possible. */
export function publishBlocker(
  pkg: Pick<SoftwarePackage, 'status' | 'hashMismatch' | 'versionRevoked' | 'productBlocked'>,
): MessageKey | undefined {
  if (pkg.hashMismatch) return 'software.disabled.hashMismatch';
  if (pkg.versionRevoked) return 'software.disabled.versionRevoked';
  if (pkg.productBlocked) return 'software.disabled.productBlocked';
  if (pkg.status === 'published') return 'software.disabled.alreadyPublished';
  if (pkg.status !== 'packaged') return 'software.disabled.packageNotReady';
  return undefined;
}

export function isSha256(value: string): boolean {
  return /^[0-9a-fA-F]{64}$/.test(value.trim());
}

export function isHttpsUrl(value: string): boolean {
  try {
    return new URL(value.trim()).protocol === 'https:';
  } catch {
    return false;
  }
}

/** A short, readable form of a hash: the first and last eight characters. */
export function shortHash(hash: string): string {
  return hash.length > 20 ? `${hash.slice(0, 8)}…${hash.slice(-8)}` : hash;
}

/** True when the provider-reported installer hash differs from the approved one. */
export function hashDiffers(approved: string, reported: string | null): boolean {
  return reported !== null && reported.toLowerCase() !== approved.toLowerCase();
}

export function isProviderNotConfigured(error: unknown): boolean {
  return isApiError(error) && error.code === 'endpoints.software_provider_not_configured';
}

export type RegisterErrors = Partial<
  Record<
    | 'productId'
    | 'version'
    | 'installerSha256'
    | 'installerUrl'
    | 'installCommand'
    | 'detectionRule',
    MessageKey
  >
>;

/** Client-side checks mirroring the server's; the server stays authoritative. */
export function validateRegister(input: {
  productId: string;
  version: string;
  installerSha256: string;
  installerUrl: string;
  installCommand: string;
  detectionRule: string;
}): RegisterErrors {
  const errors: RegisterErrors = {};
  if (!input.productId) errors.productId = 'software.validation.required';
  if (!input.version.trim()) errors.version = 'software.validation.required';
  else if (input.version.trim().length > 100) errors.version = 'software.validation.tooLong';
  if (!isSha256(input.installerSha256)) errors.installerSha256 = 'software.validation.sha256';
  if (!isHttpsUrl(input.installerUrl)) errors.installerUrl = 'software.validation.https';
  if (!input.installCommand.trim()) errors.installCommand = 'software.validation.required';
  else if (/[\r\n]/.test(input.installCommand))
    errors.installCommand = 'software.validation.oneLine';
  if (!input.detectionRule.trim()) errors.detectionRule = 'software.validation.required';
  return errors;
}
