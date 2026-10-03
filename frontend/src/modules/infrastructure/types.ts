export type Page<T> = { items: T[]; nextCursor?: string; assetReferences?: Record<string, string> };
export type Building = {
  id: string;
  siteLocationId: string;
  name: string;
  addressNote: string | null;
  active: boolean;
  version: number;
};
export type Room = {
  id: string;
  buildingId: string;
  name: string;
  floor: string | null;
  active: boolean;
  version: number;
};
export type Rack = {
  id: string;
  roomId: string;
  name: string;
  heightU: number;
  active: boolean;
  version: number;
};
export type Placement = {
  id: string;
  rackId: string;
  assetId: string;
  assetReference?: string;
  uPosition: number;
  heightU: number;
  face: 'front' | 'rear';
  version: number;
  removedAt: string | null;
  removalReason: string | null;
};
export type RackDetail = Rack & { placements: Placement[] };
export type BuildingSummary = {
  id: string;
  name: string;
  active: boolean;
  rooms: number;
  racks: number;
  placedAssets: number;
  version: number;
};
export type Site = {
  locationId: string;
  name: string;
  buildings: number;
  rooms: number;
  racks: number;
  placedAssets: number;
  buildingItems: BuildingSummary[];
};
export type Tree = { items: Site[] };
export type AssetLocation = {
  placed: boolean;
  rackId?: string;
  rackName?: string;
  roomId?: string;
  roomName?: string;
  buildingId?: string;
  buildingName?: string;
  siteName?: string;
  uPosition?: number;
  heightU?: number;
  face?: 'front' | 'rear';
};
export type VMState = 'running' | 'stopped' | 'unknown' | 'decommissioned';
export type VM = {
  id: string;
  name: string;
  state: VMState;
  hypervisorAssetId: string | null;
  hypervisorAssetReference?: string;
  vcpu: number;
  memoryMb: number;
  managementAddress: string | null;
  networkNote: string | null;
  notes: string | null;
  version: number;
  decommissionReason: string | null;
};
