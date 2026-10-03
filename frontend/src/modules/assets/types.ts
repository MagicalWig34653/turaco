// Types mirror api/openapi/openapi.yaml. Keep them in sync with the contract.

export const assetStatuses = [
  'received',
  'available',
  'reserved',
  'assigned',
  'returned',
  'in_repair',
  'retired',
  'disposed',
  'lost',
] as const;
export type AssetStatus = (typeof assetStatuses)[number];

export const provisioningStatuses = [
  'not_required',
  'not_started',
  'pending',
  'in_progress',
  'ready',
  'failed',
] as const;
export type ProvisioningStatus = (typeof provisioningStatuses)[number];

export const ownershipTypes = ['owned', 'leased', 'loaned'] as const;

export type Asset = {
  id: string;
  reference: string;
  productId: string;
  serialNumber: string | null;
  assetTag: string | null;
  status: AssetStatus;
  statusReason: string | null;
  provisioningStatus: ProvisioningStatus;
  ownershipType: (typeof ownershipTypes)[number];
  supplierId: string | null;
  purchasedAt: string | null;
  warrantyUntil: string | null;
  locationId: string | null;
  notes: string | null;
  version: number;
  createdAt: string;
  updatedAt: string;
};

export type AssetAssignment = {
  id: string;
  assigneeType: 'user' | 'team' | 'location';
  assigneeId: string;
  assignedAt: string;
  assignedBy: string | null;
  returnedAt: string | null;
  note: string | null;
};

export type AssetDetail = Asset & {
  assignments: AssetAssignment[];
  allowedOperations: string[];
  names: Record<string, string>;
};

export type AssetList = {
  items: Asset[];
  nextCursor?: string;
  productNames: Record<string, string>;
};

/** Lifecycle operations of the API, as the URL segment (kebab-case) the server uses. */
export type AssetOperation =
  | 'make_available'
  | 'assign'
  | 'reassign'
  | 'return'
  | 'send_to_repair'
  | 'finish_repair'
  | 'retire'
  | 'dispose'
  | 'mark_lost'
  | 'recover';

export const operationsWithReason: readonly string[] = [
  'send_to_repair',
  'retire',
  'dispose',
  'mark_lost',
  'recover',
];
export const operationsWithAssignee: readonly string[] = ['assign', 'reassign'];

export type AssetCreate = {
  productId: string;
  serialNumber?: string;
  assetTag?: string;
  ownershipType?: string;
  status?: 'available' | 'received';
  purchasedAt?: string;
  warrantyUntil?: string;
  notes?: string;
};

export type AssetUpdate = {
  expectedVersion: number;
  serialNumber?: string;
  assetTag?: string;
  ownershipType?: string;
  warrantyUntil?: string;
  clearWarrantyUntil?: boolean;
  notes?: string;
};
